package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"antigravity-go-proxy/internal/cloudcode"
	"antigravity-go-proxy/internal/config"
	proxyformat "antigravity-go-proxy/internal/format"
)

type mockCloudCodeBackend struct{}

func (m *mockCloudCodeBackend) FetchAvailableModels(ctx context.Context) (cloudcode.Response, error) {
	return cloudcode.Response{
		Body: []byte(`{"models":{"gemini-2.5-pro":{"displayName":"Gemini 2.5 Pro"}}}`),
	}, nil
}

func (m *mockCloudCodeBackend) StreamGenerateContent(ctx context.Context, req map[string]any, cb func(cloudcode.SSEEvent) error) (cloudcode.Response, error) {
	return cloudcode.Response{Body: []byte(`{}`)}, nil
}

func TestOpenRouterForwarding_Unary(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	var receivedAuth, receivedKey, receivedVer, receivedBeta string
	var receivedBody map[string]any

	mockOR := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("expected path /v1/messages, got %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		receivedAuth = r.Header.Get("Authorization")
		receivedKey = r.Header.Get("x-api-key")
		receivedVer = r.Header.Get("anthropic-version")
		receivedBeta = r.Header.Get("anthropic-beta")

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"text","text":"Hello from OpenRouter!"}],"model":"anthropic/claude-3.7-sonnet"}`))
	}))
	defer mockOR.Close()

	_, err := config.Save(map[string]any{
		"openrouter": map[string]any{
			"enabled": true,
			"apiKey":  "sk-or-v1-secret-123",
			"baseUrl": mockOR.URL,
			"allowlist": []map[string]any{
				{
					"id":            "anthropic/claude-3.7-sonnet",
					"alias":         "claude-3-7-openrouter",
					"displayName":   "Claude 3.7 Sonnet (OpenRouter)",
					"contextLength": 200000,
					"enabled":       true,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("config save error: %v", err)
	}

	server, err := New(Options{
		APIKey:  "test-proxy-key",
		Backend: &mockCloudCodeBackend{},
		Builder: proxyformat.NewBuilder(),
		Now:     time.Now,
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// 1. Test request using alias claude-3-7-openrouter
	reqPayload := `{"model":"claude-3-7-openrouter","messages":[{"role":"user","content":"Hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-proxy-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "output-128k-2025-02-19")

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	if receivedAuth != "Bearer sk-or-v1-secret-123" {
		t.Errorf("expected Bearer sk-or-v1-secret-123, got %s", receivedAuth)
	}
	if receivedKey != "sk-or-v1-secret-123" {
		t.Errorf("expected x-api-key sk-or-v1-secret-123, got %s", receivedKey)
	}
	if receivedVer != "2023-06-01" {
		t.Errorf("expected anthropic-version 2023-06-01, got %s", receivedVer)
	}
	if receivedBeta != "output-128k-2025-02-19" {
		t.Errorf("expected anthropic-beta output-128k-2025-02-19, got %s", receivedBeta)
	}
	if receivedBody["model"] != "anthropic/claude-3.7-sonnet" {
		t.Errorf("expected rewritten model anthropic/claude-3.7-sonnet, got %v", receivedBody["model"])
	}

	var respBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if respBody["id"] != "msg_123" {
		t.Errorf("expected response id msg_123, got %v", respBody["id"])
	}
}

func TestOpenRouterForwarding_SSE(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	mockOR := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_sse\"}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"streaming text\"}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer mockOR.Close()

	_, err := config.Save(map[string]any{
		"openrouter": map[string]any{
			"enabled": true,
			"apiKey":  "sk-or-v1-secret-123",
			"baseUrl": mockOR.URL,
			"allowlist": []map[string]any{
				{
					"id":      "anthropic/claude-3.7-sonnet",
					"enabled": true,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("config save error: %v", err)
	}

	server, err := New(Options{
		APIKey:  "test-proxy-key",
		Backend: &mockCloudCodeBackend{},
		Builder: proxyformat.NewBuilder(),
		Now:     time.Now,
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	reqPayload := `{"model":"anthropic/claude-3.7-sonnet","stream":true,"messages":[{"role":"user","content":"Hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-proxy-key")

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "message_start") || !strings.Contains(bodyStr, "streaming text") {
		t.Errorf("expected SSE chunks in response, got %s", bodyStr)
	}
}

func TestMergedModelsEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	_, err := config.Save(map[string]any{
		"openrouter": map[string]any{
			"enabled": true,
			"apiKey":  "sk-or-v1-secret-123",
			"allowlist": []map[string]any{
				{
					"id":            "anthropic/claude-3.7-sonnet",
					"alias":         "claude-3-7-openrouter",
					"displayName":   "Claude 3.7 Sonnet (OpenRouter)",
					"contextLength": 200000,
					"enabled":       true,
				},
				{
					"id":      "disabled/model",
					"enabled": false,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("config save error: %v", err)
	}

	server, err := New(Options{
		APIKey:  "test-proxy-key",
		Backend: &mockCloudCodeBackend{},
		Builder: proxyformat.NewBuilder(),
		Now:     time.Now,
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("x-api-key", "test-proxy-key")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode models response: %v", err)
	}

	foundCloudCode := false
	foundORModel := false
	foundORAlias := false
	foundDisabled := false

	for _, m := range resp.Data {
		id, _ := m["id"].(string)
		if id == "gemini-2.5-pro" {
			foundCloudCode = true
		}
		if id == "anthropic/claude-3.7-sonnet" {
			foundORModel = true
			if m["owned_by"] != "openrouter" {
				t.Errorf("expected owned_by openrouter, got %v", m["owned_by"])
			}
		}
		if id == "claude-3-7-openrouter" {
			foundORAlias = true
		}
		if id == "disabled/model" {
			foundDisabled = true
		}
	}

	if !foundCloudCode {
		t.Errorf("expected Cloud Code model gemini-2.5-pro in models list")
	}
	if !foundORModel {
		t.Errorf("expected OpenRouter model anthropic/claude-3.7-sonnet in models list")
	}
	if !foundORAlias {
		t.Errorf("expected OpenRouter alias claude-3-7-openrouter in models list")
	}
	if foundDisabled {
		t.Errorf("disabled model should not be in models list")
	}
}
