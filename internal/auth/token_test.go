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
		Path:        path,
		HTTPClient:  server.Client(),
		WriteBack:   true,
		TokenURL:    server.URL + "/token",
		UserInfoURL: server.URL + "/userinfo",
		Now:         func() time.Time { return now },
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

func assertFormValue(t *testing.T, form url.Values, key, want string) {
	t.Helper()
	if got := form.Get(key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}
