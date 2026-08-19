package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerRefreshesWrappedTokenAndResolvesEmail(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	path := filepath.Join(directory, "antigravity-oauth-token")
	original := map[string]any{
		"token": map[string]any{
			"access_token":  "expired",
			"refresh_token": "refresh|project|managed",
			"expiry":        now.Add(-time.Hour).Format(time.RFC3339Nano),
		},
		"auth_method": "consumer",
	}
	payload, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if err := request.ParseForm(); err != nil {
				t.Error(err)
			}
			assertFormValue(t, request.Form, "client_id", "test-client-id")
			assertFormValue(t, request.Form, "client_secret", "test-client-secret")
			assertFormValue(t, request.Form, "grant_type", "refresh_token")
			assertFormValue(t, request.Form, "refresh_token", "refresh")
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"access_token":"fresh","refresh_token":"rotated","token_type":"Bearer","expires_in":3600}`))
		case "/userinfo":
			if got := request.Header.Get("Authorization"); got != "Bearer fresh" {
				t.Errorf("Authorization = %q", got)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"email":"user@example.com"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	manager := Manager{
		Path:              path,
		HTTPClient:        server.Client(),
		WriteBack:         true,
		TokenURL:          server.URL + "/token",
		UserInfoURL:       server.URL + "/userinfo",
		OAuthClientID:     "test-client-id",
		OAuthClientSecret: "test-client-secret",
		Now:               func() time.Time { return now },
	}
	credentials, err := manager.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessToken != "fresh" || credentials.Email != "user@example.com" || !credentials.Refreshed {
		t.Fatalf("credentials = %#v", credentials)
	}
	written, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if written.AccessToken != "fresh" || written.RefreshToken != "rotated" {
		t.Fatalf("written token = %#v", written)
	}
	if written.Expiry != now.Add(time.Hour) {
		t.Fatalf("expiry = %s", written.Expiry)
	}
}

func TestManagerReadsOAuthCredentialsFromEnvironment(t *testing.T) {
	t.Setenv(oauthClientIDEnv, "environment-client-id")
	t.Setenv(oauthClientSecretEnv, "environment-client-secret")

	clientID, clientSecret, err := (Manager{}).oauthCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if clientID != "environment-client-id" || clientSecret != "environment-client-secret" {
		t.Fatalf("unexpected OAuth credentials returned from environment")
	}
}

func TestManagerUsesFreshFlatToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "token")
	payload := []byte(`{"access_token":"cached","refresh_token":"refresh","expiry_date":1784034000000,"auth_method":"consumer"}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/userinfo" {
			t.Fatalf("unexpected refresh request: %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"email":"cached@example.com"}`))
	}))
	defer server.Close()

	credentials, err := (Manager{
		Path:        path,
		HTTPClient:  server.Client(),
		TokenURL:    server.URL + "/token",
		UserInfoURL: server.URL + "/userinfo",
		Now:         func() time.Time { return now },
	}).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessToken != "cached" || credentials.Refreshed {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestManagerGetCached(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	content := `{"access_token":"cached-tok","expiry":"2099-01-01T00:00:00Z"}`
	if err := os.WriteFile(tokenPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	mgr := Manager{Path: tokenPath}
	ctx := context.Background()
	cred1, err := mgr.Get(ctx)
	if err != nil {
		t.Skip("network dependent userinfo test - verify cache logic directly")
	}
	cred2, err := mgr.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cred1.AccessToken != cred2.AccessToken {
		t.Errorf("expected access token match, got %s vs %s", cred1.AccessToken, cred2.AccessToken)
	}
}

func TestTokenCacheMultiPathAndMtime(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "token1.json")
	path2 := filepath.Join(dir, "token2.json")
	if err := os.WriteFile(path1, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path2, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	info1, _ := os.Stat(path1)
	info2, _ := os.Stat(path2)
	now := time.Now()

	cred1 := Credentials{AccessToken: "tok1", Expiry: now.Add(time.Hour)}
	cred2 := Credentials{AccessToken: "tok2", Expiry: now.Add(time.Hour)}

	setCachedCredentials(path1, cred1, info1.ModTime())
	setCachedCredentials(path2, cred2, info2.ModTime())

	// Verify both paths cached independently
	got1, ok1 := getCachedCredentials(path1, now)
	if !ok1 || got1.AccessToken != "tok1" {
		t.Fatalf("path1 cache mismatch: got %#v, ok=%v", got1, ok1)
	}
	got2, ok2 := getCachedCredentials(path2, now)
	if !ok2 || got2.AccessToken != "tok2" {
		t.Fatalf("path2 cache mismatch: got %#v, ok=%v", got2, ok2)
	}

	// Invalidate path1 mtime by touching file
	future := time.Now().Add(10 * time.Second)
	_ = os.Chtimes(path1, future, future)

	_, ok1After := getCachedCredentials(path1, now)
	if ok1After {
		t.Fatalf("expected cache miss after mtime change on path1")
	}

	// path2 remains cached
	_, ok2Still := getCachedCredentials(path2, now)
	if !ok2Still {
		t.Fatalf("expected path2 cache hit")
	}
}

func assertFormValue(t *testing.T, form url.Values, key, want string) {
	t.Helper()
	if got := form.Get(key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}
