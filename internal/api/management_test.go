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
	"antigravity-go-proxy/internal/stats"
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

type mockRefresherBackend struct {
	acc *accounts.Account
}

func (m *mockRefresherBackend) FetchAvailableModels(context.Context) (cloudcode.Response, error) {
	return cloudcode.Response{Body: []byte(`{"models":[]}`)}, nil
}
func (m *mockRefresherBackend) StreamGenerateContent(context.Context, map[string]any, func(cloudcode.SSEEvent) error) (cloudcode.Response, error) {
	return cloudcode.Response{}, nil
}
func (m *mockRefresherBackend) RefreshAccount(ctx context.Context, email string) (*accounts.Account, error) {
	m.acc.Subscription = accounts.Subscription{Tier: "pro", ProjectID: "proj-123"}
	return m.acc, nil
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

	t.Run("POST /api/accounts/test@example.com/refresh with refresher", func(t *testing.T) {
		acc := &accounts.Account{Email: "test@example.com", Enabled: true, Source: "manual"}
		mgr, _ := accounts.New(accounts.Options{Accounts: []*accounts.Account{acc}})
		refresherServer, _ := New(Options{
			APIKey:         "test-api-key",
			AccountManager: mgr,
			Backend:        &mockRefresherBackend{acc: acc},
		})

		req := httptest.NewRequest(http.MethodPost, "/api/accounts/test@example.com/refresh", nil)
		rec := httptest.NewRecorder()
		refresherServer.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var res map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res["status"] != "ok" {
			t.Errorf("expected status ok, got %v", res["status"])
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

func TestManagement_StatsHistory(t *testing.T) {
	tracker, err := stats.NewTracker("")
	if err != nil {
		t.Fatalf("NewTracker failed: %v", err)
	}
	tracker.Track("claude-opus-4-6")

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

	server, err := New(Options{
		APIKey:      "test-api-key",
		Credentials: func(context.Context) (auth.Credentials, error) { return auth.Credentials{AccessToken: "token"}, nil },
		NewUpstream: func(string) Upstream { return &mockUpstream{} },
		Now:         now,
		AccountManager: mgr,
		Tracker:     tracker,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	t.Run("GET /account-limits?includeHistory=true", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/account-limits?includeHistory=true", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var res map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		history, ok := res["history"].(map[string]any)
		if !ok || len(history) == 0 {
			t.Fatalf("expected history in response, got %v", res["history"])
		}
	})

	t.Run("GET /api/stats/history", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/stats/history", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var res map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res["status"] != "ok" {
			t.Errorf("expected status ok, got %v", res["status"])
		}
		history, ok := res["history"].(map[string]any)
		if !ok || len(history) == 0 {
			t.Fatalf("expected history in response, got %v", res["history"])
		}
	})
}

func TestManagement_CustomEndpointsAndModelMapping(t *testing.T) {
	server, _, _ := newTestServerWithManager(t)
	handler := server.Handler()

	t.Run("POST /api/config with customEndpoints", func(t *testing.T) {
		payload := `{"customEndpoints":{"claude-3-opus-20240229":{"url":"https://api.anthropic.com","apiKey":"secret-key"}}}`
		req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("GET /account-limits includes customEndpoints", func(t *testing.T) {
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
		ce, ok := res["customEndpoints"].(map[string]any)
		if !ok {
			t.Fatalf("expected customEndpoints in /account-limits response, got %v", res["customEndpoints"])
		}
		opusEp, exists := ce["claude-3-opus-20240229"].(map[string]any)
		if !exists {
			t.Fatalf("expected claude-3-opus-20240229 in customEndpoints, got %v", ce)
		}
		if opusEp["hasApiKey"] != true {
			t.Errorf("expected hasApiKey: true, got %v", opusEp["hasApiKey"])
		}
		if _, hasRawKey := opusEp["apiKey"]; hasRawKey {
			t.Errorf("apiKey secret should not be exposed in /account-limits")
		}
	})

	t.Run("POST /api/models/config and delete", func(t *testing.T) {
		// Add model alias
		payload := `{"modelId":"claude-opus-4-6","config":{"mapping":"claude-3-opus-20240229"}}`
		req := httptest.NewRequest(http.MethodPost, "/api/models/config", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		// Delete model alias
		delPayload := `{"modelId":"claude-opus-4-6","config":{"delete":true}}`
		delReq := httptest.NewRequest(http.MethodPost, "/api/models/config", strings.NewReader(delPayload))
		delReq.Header.Set("Content-Type", "application/json")
		delRec := httptest.NewRecorder()
		handler.ServeHTTP(delRec, delReq)

		if delRec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", delRec.Code, delRec.Body.String())
		}
		var delRes map[string]any
		if err := json.Unmarshal(delRec.Body.Bytes(), &delRes); err != nil {
			t.Fatal(err)
		}
		if delRes["deleted"] != true {
			t.Errorf("expected deleted: true, got %v", delRes)
		}
	})

	t.Run("POST /api/config updates account selection config and global threshold", func(t *testing.T) {
		payload := `{"accountSelection":{"strategy":"round-robin","weights":{"health":10}},"globalQuotaThreshold":0.15}`
		req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		selCfg := server.accountManager.GetSelectionConfig()
		if selCfg.Strategy != "round-robin" {
			t.Errorf("expected strategy round-robin, got %q", selCfg.Strategy)
		}
		if server.accountManager.GlobalQuotaThreshold() != 0.15 {
			t.Errorf("expected globalQuotaThreshold 0.15, got %v", server.accountManager.GlobalQuotaThreshold())
		}
	})
}
