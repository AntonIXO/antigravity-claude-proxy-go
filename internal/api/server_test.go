package api

import (
	"context"
	"encoding/json"
	"errors"
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
	for _, path := range []string{"/v1/models", "/anthropic/v1/models", "/v1/messages", "/anthropic/v1/messages"} {
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
		"models":{
			"gemini-3.5-flash-low":{"displayName":"Gemini Flash"},
			"gpt-oss":{"displayName":"Filtered"},
			"claude-sonnet-4-6":{"displayName":"Claude Sonnet"}
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
		if len(models) != 2 || models[0].(map[string]any)["id"] != "claude-sonnet-4-6" || models[1].(map[string]any)["id"] != "gemini-3.5-flash-low" {
			t.Fatalf("models=%#v", models)
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

func TestNewRejectsMissingAPIKey(t *testing.T) {
	t.Parallel()
	_, err := New(Options{
		Credentials: func(context.Context) (auth.Credentials, error) { return auth.Credentials{}, errors.New("unused") },
		NewUpstream: func(string) Upstream { return &fakeUpstream{} },
	})
	if err == nil {
		t.Fatal("New accepted an empty API key")
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
