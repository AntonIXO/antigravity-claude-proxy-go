package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
	"antigravity-go-proxy/internal/config"
	"antigravity-go-proxy/internal/logger"
	"antigravity-go-proxy/internal/stats"
)

type fakeUpstream struct {
	mu             sync.Mutex
	loadBody       []byte
	modelsBody     []byte
	streamData     [][]byte
	streamErr      error
	loadCalls      int
	streamCalls    int
	payloads       []map[string]any
	requestOptions []cloudcode.RequestOptions
}

func (upstream *fakeUpstream) LoadCodeAssist(context.Context, string) (cloudcode.Response, error) {
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	upstream.loadCalls++
	return cloudcode.Response{StatusCode: http.StatusOK, Body: upstream.loadBody}, nil
}

func (upstream *fakeUpstream) FetchAvailableModels(context.Context, string) (cloudcode.Response, error) {
	return cloudcode.Response{StatusCode: http.StatusOK, Body: upstream.modelsBody}, nil
}

func (upstream *fakeUpstream) StreamGenerateContent(_ context.Context, payload any, options cloudcode.RequestOptions, consume func(cloudcode.SSEEvent) error) (cloudcode.Response, error) {
	upstream.mu.Lock()
	upstream.streamCalls++
	if object, ok := payload.(map[string]any); ok {
		upstream.payloads = append(upstream.payloads, object)
	}
	upstream.requestOptions = append(upstream.requestOptions, options)
	data := append([][]byte(nil), upstream.streamData...)
	err := upstream.streamErr
	upstream.mu.Unlock()
	for _, event := range data {
		if consumeErr := consume(cloudcode.SSEEvent{Data: event}); consumeErr != nil {
			return cloudcode.Response{StatusCode: http.StatusOK}, consumeErr
		}
	}
	return cloudcode.Response{Endpoint: cloudcode.DailyEndpoint, StatusCode: http.StatusOK}, err
}

func TestMessagesCanonicalAndAnthropicAlias(t *testing.T) {
	t.Parallel()
	upstream := &fakeUpstream{streamData: standardStream()}
	handler := newTestHandler(t, upstream, "managed-project")
	for _, path := range []string{"/v1/messages", "/anthropic/v1/messages"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{
			"model":"claude-sonnet-4-6","max_tokens":128,
			"messages":[{"role":"user","content":"hello"}]
		}`))
		request.Header.Set("x-api-key", "local-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		var message map[string]any
		decodeBody(t, response.Body, &message)
		if message["type"] != "message" || message["role"] != "assistant" || message["stop_reason"] != "end_turn" {
			t.Fatalf("%s response=%#v", path, message)
		}
		content := message["content"].([]any)
		if len(content) != 1 || content[0].(map[string]any)["text"] != "FORMAT_OK" {
			t.Fatalf("%s content=%#v", path, content)
		}
	}
	if upstream.streamCalls != 2 {
		t.Fatalf("stream calls=%d", upstream.streamCalls)
	}
	first := upstream.payloads[0]
	if first["project"] != "managed-project" || first["model"] != "claude-sonnet-4-6" {
		t.Fatalf("payload=%#v", first)
	}
	firstSession := upstream.requestOptions[0].SessionID
	if firstSession == "" || upstream.requestOptions[1].SessionID != firstSession {
		t.Fatalf("session IDs=%q,%q", firstSession, upstream.requestOptions[1].SessionID)
	}
}

func TestStreamingMessagesEmitAnthropicSSE(t *testing.T) {
	t.Parallel()
	upstream := &fakeUpstream{streamData: standardStream()}
	handler := newTestHandler(t, upstream, "managed-project")
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4-6","stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	request.Header.Set("Authorization", "Bearer local-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	body := response.Body.String()
	for _, event := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"} {
		if !strings.Contains(body, "event: "+event+"\n") {
			t.Fatalf("missing %s event in %s", event, body)
		}
	}
	if !strings.Contains(body, `"text":"FORMAT_OK"`) {
		t.Fatalf("missing text delta in %s", body)
	}
}

func TestAPIKeyIsRequiredForBothPrefixes(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, &fakeUpstream{}, "project")
	for _, path := range []string{"/v1/models", "/anthropic/v1/models", "/v1/usage", "/anthropic/v1/usage", "/v1/messages", "/anthropic/v1/messages"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "messages") {
			method = http.MethodPost
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestModelsAndHealthAliases(t *testing.T) {
	t.Parallel()
	upstream := &fakeUpstream{modelsBody: []byte(`{
		"agentModelSorts":[{"groups":[{"modelIds":["gemini-3.5-flash-low","gpt-oss","claude-sonnet-4-6"]}]}],
		"models":{
			"gemini-3.5-flash-low":{"displayName":"Gemini Flash","quotaInfo":{"remainingFraction":0.875,"resetTime":"2026-07-15T18:00:00Z"}},
			"gpt-oss":{"displayName":"GPT OSS","quotaInfo":{"remainingFraction":0.5,"resetTime":"2026-07-16T00:00:00Z"}},
			"claude-sonnet-4-6":{"displayName":"Claude Sonnet","quotaInfo":{"remainingFraction":0.5,"resetTime":"2026-07-16T00:00:00Z"}}
		}
	}`)}
	handler := newTestHandler(t, upstream, "project")
	for _, path := range []string{"/v1/models", "/anthropic/v1/models"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("x-api-key", "local-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		var list map[string]any
		decodeBody(t, response.Body, &list)
		models := list["data"].([]any)
		if len(models) != 3 || models[0].(map[string]any)["id"] != "gemini-3.5-flash-low" || models[1].(map[string]any)["id"] != "gpt-oss" || models[2].(map[string]any)["id"] != "claude-sonnet-4-6" {
			t.Fatalf("models=%#v", models)
		}
	}
	for _, path := range []string{"/v1/usage", "/anthropic/v1/usage"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer local-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		var usage map[string]any
		decodeBody(t, response.Body, &usage)
		if usage["provider"] != "antigravity-proxy" || usage["source"] != "cloudcode.fetchAvailableModels" {
			t.Fatalf("usage=%#v", usage)
		}
		models := usage["models"].([]any)
		if len(models) != 3 || models[0].(map[string]any)["remaining_fraction"] != 0.875 || models[0].(map[string]any)["used_percent"] != 12.5 {
			t.Fatalf("usage models=%#v", models)
		}
		windows := usage["windows"].([]any)
		if len(windows) != 2 || windows[0].(map[string]any)["label"] != "Gemini quota" || windows[1].(map[string]any)["label"] != "GPT-OSS / Anthropic quota" {
			t.Fatalf("usage windows=%#v", windows)
		}
	}
	for _, path := range []string{"/health", "/anthropic/health"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ok"`) {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestManagedProjectIsDiscoveredAndCached(t *testing.T) {
	t.Parallel()
	upstream := &fakeUpstream{
		loadBody:   []byte(`{"cloudaicompanionProject":{"id":"discovered-project"}}`),
		streamData: standardStream(),
	}
	handler := newTestHandler(t, upstream, "")
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
			"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]
		}`))
		request.Header.Set("x-api-key", "local-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if upstream.loadCalls != 1 {
		t.Fatalf("loadCodeAssist calls=%d", upstream.loadCalls)
	}
	for _, payload := range upstream.payloads {
		if payload["project"] != "discovered-project" {
			t.Fatalf("payload project=%v", payload["project"])
		}
	}
}

func TestInitialUpstreamErrorStaysJSON(t *testing.T) {
	t.Parallel()
	upstream := &fakeUpstream{streamErr: &cloudcode.HTTPError{
		Endpoint: cloudcode.DailyEndpoint, StatusCode: http.StatusTooManyRequests,
		Status: "429 Too Many Requests", Body: "quota",
	}}
	handler := newTestHandler(t, upstream, "project")
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4-6","stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	request.Header.Set("x-api-key", "local-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "RESOURCE_EXHAUSTED") {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestAPIKeyOptionalAndEnforcedWhenConfigured(t *testing.T) {
	t.Parallel()
	upstream := &fakeUpstream{streamData: standardStream()}

	// Test 1: When APIKey is empty, requests without auth header succeed
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	serverOpen, err := New(Options{
		APIKey:    "",
		ProjectID: "test-proj",
		Now:       func() time.Time { return now },
		Credentials: func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{AccessToken: "access-token", Email: "user@example.com", Expiry: now.Add(time.Hour)}, nil
		},
		NewUpstream: func(string) Upstream { return upstream },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("failed to create server with empty APIKey: %v", err)
	}

	reqNoKey := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4-6","max_tokens":128,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	recNoKey := httptest.NewRecorder()
	serverOpen.Handler().ServeHTTP(recNoKey, reqNoKey)
	if recNoKey.Code != http.StatusOK {
		t.Fatalf("expected 200 for open proxy, got %d: %s", recNoKey.Code, recNoKey.Body.String())
	}

	// Test 2: When APIKey is configured, unauthorized requests are rejected
	serverAuth, err := New(Options{
		APIKey:    "secret-123",
		ProjectID: "test-proj",
		Now:       func() time.Time { return now },
		Credentials: func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{AccessToken: "access-token", Email: "user@example.com", Expiry: now.Add(time.Hour)}, nil
		},
		NewUpstream: func(string) Upstream { return upstream },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("failed to create server with APIKey: %v", err)
	}

	recUnauth := httptest.NewRecorder()
	serverAuth.Handler().ServeHTTP(recUnauth, reqNoKey)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized request, got %d", recUnauth.Code)
	}

	// Request with correct x-api-key succeeds
	reqWithKey := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4-6","max_tokens":128,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	reqWithKey.Header.Set("x-api-key", "secret-123")
	recAuth := httptest.NewRecorder()
	serverAuth.Handler().ServeHTTP(recAuth, reqWithKey)
	if recAuth.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid key, got %d: %s", recAuth.Code, recAuth.Body.String())
	}
}

func newTestHandler(t *testing.T, upstream *fakeUpstream, projectID string) http.Handler {
	t.Helper()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	server, err := New(Options{
		APIKey: "local-key", ProjectID: projectID, Now: func() time.Time { return now },
		Credentials: func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{AccessToken: "access-token", Email: "user@example.com", Expiry: now.Add(time.Hour)}, nil
		},
		NewUpstream: func(token string) Upstream {
			if token != "access-token" {
				t.Fatalf("access token=%q", token)
			}
			return upstream
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler()
}

func standardStream() [][]byte {
	return [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"FORMAT_OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}}`),
	}
}

func decodeBody(t *testing.T, reader io.Reader, destination any) {
	t.Helper()
	if err := json.NewDecoder(reader).Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func TestLoggingMiddleware(t *testing.T) {
	broadcaster := logger.NewBroadcaster(10)
	streamHandler := logger.NewStreamHandler(nil, broadcaster)
	testLogger := slog.New(streamHandler)

	handler := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/not-found" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}), testLogger)

	// 1. Success request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// 2. Warn request
	reqWarn := httptest.NewRequest(http.MethodGet, "/not-found", nil)
	recWarn := httptest.NewRecorder()
	handler.ServeHTTP(recWarn, reqWarn)

	// 3. Error request
	reqErr := httptest.NewRequest(http.MethodGet, "/error", nil)
	recErr := httptest.NewRecorder()
	handler.ServeHTTP(recErr, reqErr)

	// 4. Skipped request
	reqSkip := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	recSkip := httptest.NewRecorder()
	handler.ServeHTTP(recSkip, reqSkip)

	history := broadcaster.GetHistory()
	if len(history) != 3 {
		t.Fatalf("expected 3 logged requests (skipped count_tokens), got %d", len(history))
	}
	if history[0].Level != "INFO" || !strings.Contains(history[0].Message, "[GET] /test 200") {
		t.Errorf("expected INFO log for /test 200, got %+v", history[0])
	}
	if history[1].Level != "WARN" || !strings.Contains(history[1].Message, "[GET] /not-found 404") {
		t.Errorf("expected WARN log for /not-found 404, got %+v", history[1])
	}
	if history[2].Level != "ERROR" || !strings.Contains(history[2].Message, "[GET] /error 500") {
		t.Errorf("expected ERROR log for /error 500, got %+v", history[2])
	}
}

func TestTrackerIntegration(t *testing.T) {
	tracker, err := stats.NewTracker("")
	if err != nil {
		t.Fatalf("NewTracker failed: %v", err)
	}

	upstream := &fakeUpstream{streamData: standardStream()}
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	server, err := New(Options{
		APIKey:    "local-key",
		ProjectID: "test-proj",
		Now:       func() time.Time { return now },
		Credentials: func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{AccessToken: "access-token", Email: "user@example.com", Expiry: now.Add(time.Hour)}, nil
		},
		NewUpstream: func(string) Upstream { return upstream },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tracker:     tracker,
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// 1. Unary message
	reqUnary := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-3-5-sonnet-20241022","max_tokens":128,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	reqUnary.Header.Set("x-api-key", "local-key")
	recUnary := httptest.NewRecorder()
	server.Handler().ServeHTTP(recUnary, reqUnary)
	if recUnary.Code != http.StatusOK {
		t.Fatalf("unary request failed: %d", recUnary.Code)
	}

	// 2. Stream message
	reqStream := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gemini-2.5-flash","stream":true,"max_tokens":128,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	reqStream.Header.Set("x-api-key", "local-key")
	recStream := httptest.NewRecorder()
	server.Handler().ServeHTTP(recStream, reqStream)
	if recStream.Code != http.StatusOK {
		t.Fatalf("stream request failed: %d", recStream.Code)
	}

	history := tracker.GetHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 hour bucket in stats tracker, got %d", len(history))
	}
	var hourMap map[string]any
	for _, v := range history {
		hourMap = v.(map[string]any)
	}
	if hourMap["_total"] != 2 {
		t.Errorf("expected total requests 2, got %v", hourMap["_total"])
	}
	claudeMap := hourMap["claude"].(map[string]int)
	if claudeMap["3-5-sonnet-20241022"] != 1 {
		t.Errorf("expected claude count 1, got %d", claudeMap["3-5-sonnet-20241022"])
	}
	geminiMap := hourMap["gemini"].(map[string]int)
	if geminiMap["2.5-flash"] != 1 {
		t.Errorf("expected gemini count 1, got %d", geminiMap["2.5-flash"])
	}
}

func TestModelMappingRedirection(t *testing.T) {
	// Setup configuration with a mapping from custom-object-source (object) and gemini-custom-source (string)
	cfg := config.DefaultConfig()
	cfg.ModelMapping = map[string]any{
		"custom-object-source": map[string]any{
			"mapping": "gpt-oss",
		},
		"gemini-custom-source": "gemini-3.5-flash-low",
	}
	// Save/Apply config
	config.Save(map[string]any{"modelMapping": cfg.ModelMapping})
	defer func() {
		// Cleanup config mapping
		config.Save(map[string]any{"modelMapping": make(map[string]any)})
	}()

	upstream := &fakeUpstream{
		loadBody:   []byte(`{"cloudaicompanionProject":{"id":"discovered-project"}}`),
		streamData: standardStream(),
	}
	handler := newTestHandler(t, upstream, "test-proj")

	// Test 1: Object mapping
	request1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"custom-object-source","messages":[{"role":"user","content":"hello"}]
	}`))
	request1.Header.Set("x-api-key", "local-key")
	response1 := httptest.NewRecorder()
	handler.ServeHTTP(response1, request1)

	if response1.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response1.Code, response1.Body.String())
	}
	if len(upstream.payloads) == 0 {
		t.Fatal("no payload sent to upstream")
	}
	sentModel1 := upstream.payloads[0]["model"]
	if sentModel1 != "gpt-oss" {
		t.Fatalf("expected model to be mapped to gpt-oss, got %v", sentModel1)
	}

	// Test 2: String mapping
	request2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gemini-custom-source","messages":[{"role":"user","content":"hello"}]
	}`))
	request2.Header.Set("x-api-key", "local-key")
	response2 := httptest.NewRecorder()
	handler.ServeHTTP(response2, request2)

	if response2.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response2.Code, response2.Body.String())
	}
	if len(upstream.payloads) < 2 {
		t.Fatal("second payload not sent to upstream")
	}
	sentModel2 := upstream.payloads[1]["model"]
	if sentModel2 != "gemini-3.5-flash-low" {
		t.Fatalf("expected model to be mapped to gemini-3.5-flash-low, got %v", sentModel2)
	}
}
