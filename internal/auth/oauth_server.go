package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	OAuthAuthURL        = "https://accounts.google.com/o/oauth2/v2/auth"
	OAuthTokenURL       = "https://oauth2.googleapis.com/token"
	OAuthUserInfoURL    = "https://www.googleapis.com/oauth2/v1/userinfo"
	DefaultCallbackPort = 51121
)

func resolveOAuthCredentials() (string, string, error) {
	if id, secret := os.Getenv(oauthClientIDEnv), os.Getenv(oauthClientSecretEnv); id != "" && secret != "" {
		return id, secret, nil
	}
	return AgyOAuthCredentials()
}

var OAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// AccountSaver is implemented by account managers to persist OAuth credentials.
type AccountSaver interface {
	SaveOAuthAccount(email, refreshToken string) error
}

type AccountSaverFunc func(email, refreshToken string) error

func (f AccountSaverFunc) SaveOAuthAccount(email, refreshToken string) error {
	return f(email, refreshToken)
}

// GeneratePKCE creates a PKCE code_verifier and code_challenge (S256).
func GeneratePKCE() (verifier string, challenge string, err error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", fmt.Errorf("generate random bytes: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(bytes)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// GenerateState creates a random hex string for state validation.
func GenerateState() string {
	bytes := make([]byte, 16)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// GetAuthorizationURL builds the Google OAuth authorization URL.
func GetAuthorizationURL(customRedirectURI string) (authURL, verifier, state string, err error) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return "", "", "", err
	}
	state = GenerateState()

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", DefaultCallbackPort)
	if customRedirectURI != "" {
		redirectURI = customRedirectURI
	}

	clientID, _, err := resolveOAuthCredentials()
	if err != nil || clientID == "" {
		return "", "", "", fmt.Errorf("resolve OAuth client credentials: %w", err)
	}

	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(OAuthScopes, " "))
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)

	return fmt.Sprintf("%s?%s", OAuthAuthURL, params.Encode()), verifier, state, nil
}

// PendingFlow tracks active OAuth session.
type PendingFlow struct {
	Verifier  string
	State     string
	Port      int
	Server    *http.Server
	CreatedAt time.Time
}

// AuthResult holds the returned tokens and user identity from OAuth flow.
type AuthResult struct {
	Email        string
	RefreshToken string
	AccessToken  string
}

// OAuthManager manages browser-based OAuth flows and callback listeners.
type OAuthManager struct {
	mu           sync.Mutex
	accountSaver AccountSaver
	flows        map[string]*PendingFlow
}

// NewOAuthManager creates a new OAuthManager.
func NewOAuthManager(accountSaver AccountSaver) *OAuthManager {
	return &OAuthManager{
		accountSaver: accountSaver,
		flows:        make(map[string]*PendingFlow),
	}
}

// StartFlow starts an ephemeral callback server and returns the authorization URL.
func (om *OAuthManager) StartFlow() (string, string, error) {
	om.mu.Lock()
	defer om.mu.Unlock()

	// Clean up any existing active flows and close any open listeners
	for state, f := range om.flows {
		if f.Server != nil {
			_ = f.Server.Close()
		}
		delete(om.flows, state)
	}

	portsToTry := []int{DefaultCallbackPort, 51122, 51123, 51124, 51125, 51126}
	var listener net.Listener
	var boundPort int
	for _, port := range portsToTry {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			listener = l
			boundPort = port
			break
		}
	}
	if listener == nil {
		return "", "", errors.New("could not bind to any OAuth callback port (51121-51126)")
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", boundPort)
	authURL, verifier, state, err := GetAuthorizationURL(redirectURI)
	if err != nil {
		_ = listener.Close()
		return "", "", err
	}

	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux}

	flow := &PendingFlow{
		Verifier:  verifier,
		State:     state,
		Port:      boundPort,
		Server:    srv,
		CreatedAt: time.Now(),
	}
	om.flows[state] = flow

	mux.HandleFunc("/oauth-callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := q.Get("code")
		cbState := q.Get("state")
		errParam := q.Get("error")

		if errParam != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body><h1>❌ Authentication Failed</h1><p>%s</p></body></html>", errParam)
			go func() { _ = srv.Close() }()
			return
		}

		om.mu.Lock()
		activeFlow, exists := om.flows[cbState]
		if !exists {
			om.mu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body><h1>❌ State Mismatch</h1><p>CSRF check failed.</p></body></html>")
			go func() { _ = srv.Close() }()
			return
		}
		flowVerifier := activeFlow.Verifier
		delete(om.flows, cbState)
		om.mu.Unlock()

		if code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body><h1>❌ Missing Code</h1></body></html>")
			go func() { _ = srv.Close() }()
			return
		}

		// Complete flow in background
		go func() {
			defer func() { _ = srv.Close() }()
			_, _ = om.CompleteFlow(code, flowVerifier)
		}()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "<html><body style=\"font-family:system-ui;text-align:center;padding:40px;\"><h1 style=\"color:#28a745;\">✅ Authentication Successful!</h1><p>You can close this window.</p><script>setTimeout(()=>window.close(),2000);</script></body></html>")
	})

	go func() {
		_ = srv.Serve(listener)
	}()

	return authURL, state, nil
}

// CompleteFlow exchanges code for tokens, gets user info, and saves account.
func (om *OAuthManager) CompleteFlow(code, verifier string) (*AuthResult, error) {
	clientID, clientSecret, err := resolveOAuthCredentials()
	if err != nil || clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("resolve OAuth client credentials: %w", err)
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", DefaultCallbackPort)

	// Token exchange
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)

	resp, err := http.PostForm(OAuthTokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenRes struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenRes); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	if tokenRes.AccessToken == "" {
		return nil, errors.New("no access token received")
	}

	// Fetch user email
	email, err := FetchUserEmail(tokenRes.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("fetch user email: %w", err)
	}

	result := &AuthResult{
		Email:        email,
		RefreshToken: tokenRes.RefreshToken,
		AccessToken:  tokenRes.AccessToken,
	}

	if om.accountSaver != nil {
		if err := om.accountSaver.SaveOAuthAccount(email, tokenRes.RefreshToken); err != nil {
			return nil, fmt.Errorf("save account: %w", err)
		}
	}

	return result, nil
}

// FetchUserEmail queries Google userinfo endpoint for email address.
func FetchUserEmail(accessToken string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, OAuthUserInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("userinfo request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("userinfo failed (%d): %s", resp.StatusCode, string(body))
	}

	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decode userinfo response: %w", err)
	}
	if info.Email == "" {
		return "", errors.New("no email returned in userinfo response")
	}
	return info.Email, nil
}

// ServeHTTP implements http.Handler for WebUI /api/auth/url and /api/auth/complete.
func (om *OAuthManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.URL.Path == "/api/auth/url" && r.Method == http.MethodGet {
		authURL, state, err := om.StartFlow()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"url":    authURL,
			"state":  state,
		})
		return
	}

	if r.URL.Path == "/api/auth/complete" && r.Method == http.MethodPost {
		var body struct {
			CallbackInput string `json:"callbackInput"`
			State         string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.CallbackInput == "" || body.State == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "Missing callbackInput or state"})
			return
		}

		om.mu.Lock()
		flow, exists := om.flows[body.State]
		if !exists {
			om.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "OAuth flow expired or not found"})
			return
		}
		delete(om.flows, body.State)
		if flow.Server != nil {
			_ = flow.Server.Close()
		}
		om.mu.Unlock()

		code := body.CallbackInput
		if strings.Contains(code, "code=") {
			u, err := url.Parse(code)
			if err == nil {
				if parsedCode := u.Query().Get("code"); parsedCode != "" {
					code = parsedCode
				}
			}
		}

		acc, err := om.CompleteFlow(code, flow.Verifier)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": err.Error()})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"email":   acc.Email,
			"message": fmt.Sprintf("Account %s added successfully", acc.Email),
		})
		return
	}

	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "Not found"})
}

// OpenBrowser opens the URL in the user's default browser.
func OpenBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
		args = []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
