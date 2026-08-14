package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPKCEAndState(t *testing.T) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE error: %v", err)
	}
	if len(verifier) == 0 || len(challenge) == 0 {
		t.Fatalf("empty verifier or challenge")
	}
	state := GenerateState()
	if len(state) != 32 {
		t.Fatalf("expected 32 hex chars for state, got %d", len(state))
	}
}

func TestGetAuthorizationURL(t *testing.T) {
	authURL, verifier, state, err := GetAuthorizationURL("")
	if err != nil {
		t.Fatalf("GetAuthorizationURL error: %v", err)
	}
	if !strings.HasPrefix(authURL, OAuthAuthURL) {
		t.Errorf("unexpected auth URL prefix: %s", authURL)
	}
	if !strings.Contains(authURL, "code_challenge=") || !strings.Contains(authURL, "state="+state) {
		t.Errorf("auth URL missing parameters: %s", authURL)
	}
	if len(verifier) == 0 {
		t.Errorf("empty verifier")
	}
}

func TestOAuthManager_HTTPHandler(t *testing.T) {
	saver := AccountSaverFunc(func(email, refreshToken string) error {
		return nil
	})
	om := NewOAuthManager(saver)

	t.Run("GET /api/auth/url", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/url", nil)
		rec := httptest.NewRecorder()
		om.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var res struct {
			Status string `json:"status"`
			URL    string `json:"url"`
			State  string `json:"state"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res.Status != "ok" || res.URL == "" || res.State == "" {
			t.Errorf("invalid auth url response: %+v", res)
		}
	})

	t.Run("POST /api/auth/complete with invalid state", func(t *testing.T) {
		body := map[string]string{
			"callbackInput": "4/code123",
			"state":         "non-existent-state",
		}
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/complete", bytes.NewReader(data))
		rec := httptest.NewRecorder()
		om.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})
}
