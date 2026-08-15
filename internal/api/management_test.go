package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"antigravity-go-proxy/internal/accounts"
	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
	"antigravity-go-proxy/internal/logger"
)

func newTestServerWithManager(t *testing.T) (*Server, *accounts.Manager, *logger.Broadcaster) {
	acc := &accounts.Account{
		Email:   "test@example.com",
		Source:  "manual",
		Enabled: true,
		APIKey:  "key-123",
	}
	now := func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }
	mgr, err := accounts.New(accounts.Options{
		Accounts: []*accounts.Account{acc},
		Strategy: accounts.StrategyHybrid,
		Now:      now,
	})
	if err != nil {
		t.Fatal(err)
	}

	broadcaster := logger.NewBroadcaster(100)

	server, err := New(Options{
		APIKey:      "test-api-key",
		Credentials: func(context.Context) (auth.Credentials, error) { return auth.Credentials{AccessToken: "token"}, nil },
		NewUpstream: func(string) Upstream { return &mockUpstream{} },
		Now:         now,
		AccountManager: mgr,
		Broadcaster: broadcaster,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, mgr, broadcaster
}

type mockUpstream struct{}

func (m *mockUpstream) LoadCodeAssist(context.Context, string) (cloudcode.Response, error) {
	return cloudcode.Response{Body: []byte(`{"cloudaicompanionProject":"proj-123"}`)}, nil
}
func (m *mockUpstream) FetchAvailableModels(context.Context, string) (cloudcode.Response, error) {
	return cloudcode.Response{Body: []byte(`{"models":[]}`)}, nil
}
func (m *mockUpstream) StreamGenerateContent(context.Context, any, cloudcode.RequestOptions, func(cloudcode.SSEEvent) error) (cloudcode.Response, error) {
	return cloudcode.Response{}, nil
}

func TestManagement_HealthAndLimits(t *testing.T) {
	server, _, _ := newTestServerWithManager(t)
	handler := server.Handler()

	t.Run("GET /health", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var res map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res["status"] != "ok" {
			t.Errorf("expected status ok")
		}
	})

	t.Run("GET /account-limits JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/account-limits", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var res map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res["status"] != "ok" {
			t.Errorf("expected status ok")
		}
		if res["totalAccounts"].(float64) != 1 {
			t.Errorf("expected totalAccounts 1, got %v", res["totalAccounts"])
		}
		accounts, ok := res["accounts"].([]any)
		if !ok || len(accounts) == 0 {
			t.Fatal("expected accounts array in response")
		}
		firstAcc, ok := accounts[0].(map[string]any)
		if !ok {
			t.Fatal("expected account map")
		}
		if _, hasLimits := firstAcc["limits"]; !hasLimits {
			t.Error("expected 'limits' key on account object")
		}
	})

	t.Run("GET /account-limits table", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/account-limits?format=table", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "test@example.com") {
			t.Errorf("expected table to contain test email: %s", rec.Body.String())
		}
	})

	t.Run("POST /refresh-token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/refresh-token", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})
}

func TestManagement_AccountsCRUD(t *testing.T) {
	server, _, _ := newTestServerWithManager(t)
	handler := server.Handler()

	t.Run("GET /api/accounts", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var res map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		summary, _ := res["summary"].(map[string]any)
		if summary["total"].(float64) != 1 {
			t.Errorf("expected 1 account, got %v", summary["total"])
		}
	})

	t.Run("POST /api/accounts/test@example.com/toggle", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/accounts/test@example.com/toggle", strings.NewReader(`{"enabled":false}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("POST /api/accounts/test@example.com/refresh", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/accounts/test@example.com/refresh", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("POST /api/accounts/import", func(t *testing.T) {
		body := `[{"email":"imported@example.com","refresh_token":"tok-123"}]`
		req := httptest.NewRequest(http.MethodPost, "/api/accounts/import", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("GET /api/accounts/export", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/accounts/export", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})
}

func TestManagement_LogsAndStreaming(t *testing.T) {
	server, _, broadcaster := newTestServerWithManager(t)
	handler := server.Handler()

	broadcaster.Add(logger.LogEntry{
		Timestamp: "2026-08-14T12:00:00Z",
		Level:     "INFO",
		Message:   "hello log test",
	})

	t.Run("GET /api/logs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var res struct {
			Status string            `json:"status"`
			Logs   []logger.LogEntry `json:"logs"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Logs) != 1 || res.Logs[0].Message != "hello log test" {
			t.Errorf("unexpected logs response: %+v", res)
		}
	})

	t.Run("GET /api/logs/stream with history", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		req := httptest.NewRequest(http.MethodGet, "/api/logs/stream?history=true", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if !strings.Contains(rec.Body.String(), "hello log test") {
			t.Errorf("expected SSE stream to contain historical log")
		}
	})
}

func TestManagement_ConfigAndClaude(t *testing.T) {
	server, _, _ := newTestServerWithManager(t)
	handler := server.Handler()

	t.Run("GET /api/config", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("POST /api/event_logging/batch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/event_logging/batch", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("GET /api/strategy/health", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/strategy/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})
}
