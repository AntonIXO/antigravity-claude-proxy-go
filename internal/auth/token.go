package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	clientID      = "REMOVED-OAUTH-CLIENT-ID"
	clientSecret  = "REMOVED-OAUTH-CLIENT-SECRET"
	tokenURL      = "https://oauth2.googleapis.com/token"
	userInfoURL   = "https://www.googleapis.com/oauth2/v1/userinfo"
	freshnessSkew = time.Minute
)

// Token is the normalized form of agy's wrapped or flat OAuth token file.
type Token struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	AuthMethod   string
	Expiry       time.Time
	Path         string
}

// Credentials are a usable OAuth access token and its associated account.
type Credentials struct {
	AccessToken string
	Email       string
	Expiry      time.Time
	Refreshed   bool
}

// Manager reads and refreshes the OAuth token written by agy.
type Manager struct {
	Path        string
	HTTPClient  *http.Client
	WriteBack   bool
	TokenURL    string
	UserInfoURL string
	Now         func() time.Time
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

// FromRefreshToken refreshes an explicitly configured OAuth account without
// touching agy's token file. Composite Node-proxy refresh tokens are accepted;
// only the first pipe-delimited segment is sent to Google.
func (m Manager) FromRefreshToken(ctx context.Context, refreshToken, email string) (Credentials, error) {
	if refreshToken == "" {
		return Credentials{}, errors.New("OAuth account has no refresh_token")
	}
	response, err := m.refresh(ctx, refreshToken)
	if err != nil {
		return Credentials{}, err
	}
	if email == "" {
		email, err = m.userEmail(ctx, response.AccessToken)
		if err != nil {
			return Credentials{}, err
		}
	}
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	return Credentials{
		AccessToken: response.AccessToken, Email: email,
		Expiry: now().Add(time.Duration(response.ExpiresIn) * time.Second), Refreshed: true,
	}, nil
}

// DefaultTokenPath follows the same explicit-env-GEMINI_HOME-default order as
// the Node proxy's agy token reader.
func DefaultTokenPath() (string, error) {
	if explicit := os.Getenv("AGY_TOKEN_PATH"); explicit != "" {
		return explicit, nil
	}
	geminiHome := os.Getenv("GEMINI_HOME")
	if geminiHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		geminiHome = filepath.Join(home, ".gemini")
	}
	return filepath.Join(geminiHome, "antigravity-cli", "antigravity-oauth-token"), nil
}

// Read returns the current token while holding a shared advisory lock.
func Read(path string) (Token, error) {
	file, err := os.Open(path)
	if err != nil {
		return Token{}, fmt.Errorf("open agy token: %w", err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_SH); err != nil {
		return Token{}, fmt.Errorf("lock agy token: %w", err)
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN) //nolint:errcheck

	raw, err := io.ReadAll(file)
	if err != nil {
		return Token{}, fmt.Errorf("read agy token: %w", err)
	}
	return parseToken(raw, path)
}

// Fresh reports whether an access token remains valid beyond the safety skew.
func (t Token) Fresh(now time.Time) bool {
	return t.AccessToken != "" && !t.Expiry.IsZero() && t.Expiry.Sub(now) > freshnessSkew
}

// Get returns a fresh token and resolves its account email. Refresh and
// optional write-back happen under an exclusive file lock so multiple proxy
// processes cannot refresh or overwrite the agy token concurrently.
func (m Manager) Get(ctx context.Context) (Credentials, error) {
	path := m.Path
	if path == "" {
		var err error
		path, err = DefaultTokenPath()
		if err != nil {
			return Credentials{}, err
		}
	}
	flags := os.O_RDONLY
	if m.WriteBack || os.Getenv("AGY_TOKEN_WRITEBACK") == "1" {
		flags = os.O_RDWR
	}
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return Credentials{}, fmt.Errorf("open agy token: %w", err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return Credentials{}, fmt.Errorf("lock agy token: %w", err)
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN) //nolint:errcheck

	raw, err := io.ReadAll(file)
	if err != nil {
		return Credentials{}, fmt.Errorf("read agy token: %w", err)
	}
	token, err := parseToken(raw, path)
	if err != nil {
		return Credentials{}, err
	}

	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	refreshed := false
	if !token.Fresh(now()) {
		if token.RefreshToken == "" {
			return Credentials{}, errors.New("agy token is expired and has no refresh_token")
		}
		response, refreshErr := m.refresh(ctx, token.RefreshToken)
		if refreshErr != nil {
			return Credentials{}, refreshErr
		}
		token.AccessToken = response.AccessToken
		if response.RefreshToken != "" {
			token.RefreshToken = response.RefreshToken
		}
		if response.TokenType != "" {
			token.TokenType = response.TokenType
		}
		token.Expiry = now().Add(time.Duration(response.ExpiresIn) * time.Second)
		refreshed = true

		if m.WriteBack || os.Getenv("AGY_TOKEN_WRITEBACK") == "1" {
			if err := writeToken(path, raw, token); err != nil {
				return Credentials{}, fmt.Errorf("write back agy token: %w", err)
			}
		}
	}

	email, err := m.userEmail(ctx, token.AccessToken)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{
		AccessToken: token.AccessToken,
		Email:       email,
		Expiry:      token.Expiry,
		Refreshed:   refreshed,
	}, nil
}

func (m Manager) refresh(ctx context.Context, refreshToken string) (tokenResponse, error) {
	endpoint := m.TokenURL
	if endpoint == "" {
		endpoint = tokenURL
	}
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {strings.SplitN(refreshToken, "|", 2)[0]},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create token refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := m.client().Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("refresh access token: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("read token refresh response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("token refresh failed (%s): %s", response.Status, strings.TrimSpace(string(body)))
	}
	var refreshed tokenResponse
	if err := json.Unmarshal(body, &refreshed); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token refresh response: %w", err)
	}
	if refreshed.AccessToken == "" || refreshed.ExpiresIn <= 0 {
		return tokenResponse{}, errors.New("token refresh response omitted access_token or expires_in")
	}
	return refreshed, nil
}

func (m Manager) userEmail(ctx context.Context, accessToken string) (string, error) {
	endpoint := m.UserInfoURL
	if endpoint == "" {
		endpoint = userInfoURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := m.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve account email: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read userinfo response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("userinfo failed (%s): %s", response.Status, strings.TrimSpace(string(body)))
	}
	var user struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return "", fmt.Errorf("decode userinfo response: %w", err)
	}
	if user.Email == "" {
		return "", errors.New("userinfo response omitted email")
	}
	return user.Email, nil
}

func (m Manager) client() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return http.DefaultClient
}

func parseToken(raw []byte, path string) (Token, error) {
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return Token{}, fmt.Errorf("parse agy token: %w", err)
	}
	tokenMap := document
	if nested, ok := document["token"].(map[string]any); ok {
		tokenMap = nested
	}
	token := Token{
		AccessToken:  stringValue(tokenMap["access_token"]),
		RefreshToken: stringValue(tokenMap["refresh_token"]),
		TokenType:    stringValue(tokenMap["token_type"]),
		AuthMethod:   stringValue(document["auth_method"]),
		Path:         path,
	}
	if token.AuthMethod == "" {
		token.AuthMethod = stringValue(tokenMap["auth_method"])
	}
	if expiry := stringValue(tokenMap["expiry"]); expiry != "" {
		parsed, err := time.Parse(time.RFC3339Nano, expiry)
		if err != nil {
			return Token{}, fmt.Errorf("parse agy token expiry %q: %w", expiry, err)
		}
		token.Expiry = parsed
	} else if milliseconds, ok := numberValue(tokenMap["expiry_date"]); ok {
		token.Expiry = time.UnixMilli(milliseconds)
	}
	return token, nil
}

func writeToken(path string, original []byte, token Token) error {
	var document map[string]any
	if err := json.Unmarshal(original, &document); err != nil {
		return err
	}
	tokenMap := document
	if _, wrapped := document["token"]; wrapped {
		nested, ok := document["token"].(map[string]any)
		if !ok {
			nested = make(map[string]any)
			document["token"] = nested
		}
		tokenMap = nested
	}
	tokenMap["access_token"] = token.AccessToken
	tokenMap["refresh_token"] = token.RefreshToken
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	tokenMap["token_type"] = token.TokenType
	tokenMap["expiry"] = token.Expiry.UTC().Format(time.RFC3339Nano)
	delete(tokenMap, "expiry_date")

	payload, err := json.Marshal(document)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".agy-token-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func numberValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case json.Number:
		number, err := typed.Int64()
		return number, err == nil
	case string:
		number, err := strconv.ParseInt(typed, 10, 64)
		return number, err == nil
	default:
		return 0, false
	}
}
