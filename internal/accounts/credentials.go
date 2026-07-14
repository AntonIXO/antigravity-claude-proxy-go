package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"antigravity-go-proxy/internal/auth"
)

const tokenCacheTTL = 5 * time.Minute

type cachedCredential struct {
	credentials auth.Credentials
	extractedAt time.Time
}

type CredentialResolver struct {
	mu      sync.Mutex
	cache   map[string]cachedCredential
	now     func() time.Time
	manager auth.Manager
}

func NewCredentialResolver(manager auth.Manager, now func() time.Time) *CredentialResolver {
	if now == nil {
		now = time.Now
	}
	return &CredentialResolver{cache: make(map[string]cachedCredential), now: now, manager: manager}
}

func (resolver *CredentialResolver) Resolve(ctx context.Context, account *Account) (auth.Credentials, error) {
	resolver.mu.Lock()
	entry, exists := resolver.cache[account.Email]
	if exists && resolver.now().Sub(entry.extractedAt) < tokenCacheTTL &&
		(entry.credentials.Expiry.IsZero() || entry.credentials.Expiry.Sub(resolver.now()) > time.Minute) {
		resolver.mu.Unlock()
		return entry.credentials, nil
	}
	resolver.mu.Unlock()

	var credentials auth.Credentials
	var err error
	switch account.Source {
	case "agy":
		manager := resolver.manager
		manager.Path = account.AgyTokenPath
		credentials, err = manager.Get(ctx)
	case "oauth":
		credentials, err = resolver.manager.FromRefreshToken(ctx, account.RefreshToken, account.Email)
	case "manual":
		if account.APIKey == "" {
			err = errors.New("manual account has no apiKey")
		} else {
			credentials = auth.Credentials{AccessToken: account.APIKey, Email: account.Email, Expiry: resolver.now().Add(tokenCacheTTL)}
		}
	case "database", "":
		credentials, err = readDatabaseCredential(ctx, account, resolver.now())
	default:
		err = fmt.Errorf("unsupported account source %q", account.Source)
	}
	if err != nil {
		return auth.Credentials{}, fmt.Errorf("credentials for %s: %w", account.Email, err)
	}
	if credentials.Email == "" {
		credentials.Email = account.Email
	}
	resolver.mu.Lock()
	resolver.cache[account.Email] = cachedCredential{credentials: credentials, extractedAt: resolver.now()}
	resolver.mu.Unlock()
	return credentials, nil
}

func (resolver *CredentialResolver) Invalidate(email string) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	delete(resolver.cache, email)
}

func readDatabaseCredential(ctx context.Context, account *Account, now time.Time) (auth.Credentials, error) {
	path := account.DBPath
	if path == "" {
		var err error
		path, err = defaultDatabasePath()
		if err != nil {
			return auth.Credentials{}, err
		}
	}
	command := exec.CommandContext(ctx, "sqlite3", "-readonly", path,
		"SELECT value FROM ItemTable WHERE key = 'antigravityAuthStatus';")
	output, err := command.Output()
	if err != nil {
		return auth.Credentials{}, fmt.Errorf("read Antigravity database: %w", err)
	}
	var status struct {
		APIKey string `json:"apiKey"`
		Email  string `json:"email"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &status); err != nil {
		return auth.Credentials{}, fmt.Errorf("decode Antigravity auth status: %w", err)
	}
	if status.APIKey == "" {
		return auth.Credentials{}, errors.New("Antigravity auth status has no apiKey")
	}
	if status.Email == "" {
		status.Email = account.Email
	}
	return auth.Credentials{AccessToken: status.APIKey, Email: status.Email, Expiry: now.Add(tokenCacheTTL)}, nil
}

func defaultDatabasePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "globalStorage", "state.vscdb"), nil
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Antigravity", "User", "globalStorage", "state.vscdb"), nil
	default:
		return filepath.Join(home, ".config", "Antigravity", "User", "globalStorage", "state.vscdb"), nil
	}
}
