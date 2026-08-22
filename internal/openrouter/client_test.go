package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "https://openrouter.ai/api"},
		{"https://openrouter.ai/api", "https://openrouter.ai/api"},
		{"https://openrouter.ai/api/", "https://openrouter.ai/api"},
		{"https://openrouter.ai/api/v1", "https://openrouter.ai/api"},
		{"https://openrouter.ai/api/v1/", "https://openrouter.ai/api"},
		{"http://custom-openrouter.internal", "http://custom-openrouter.internal"},
		{"http://custom-openrouter.internal/v1", "http://custom-openrouter.internal"},
	}

	for _, tt := range tests {
		got := NormalizeBaseURL(tt.input)
		if got != tt.expected {
			t.Errorf("NormalizeBaseURL(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestFetchAvailableModels_Success(t *testing.T) {
	mockModels := []ModelItem{
		{
			ID:            "anthropic/claude-3.7-sonnet",
			Name:          "Anthropic: Claude 3.7 Sonnet",
			Description:   "Claude 3.7 Sonnet with hybrid reasoning",
			ContextLength: 200000,
		},
		{
			ID:            "openai/gpt-4o",
			Name:          "OpenAI: GPT-4o",
			Description:   "Omni model by OpenAI",
			ContextLength: 128000,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("expected path /v1/models, got %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Errorf("expected Bearer test-api-key, got %s", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ModelsResponse{Data: mockModels})
	}))
	defer server.Close()

	client := NewClient(5*time.Second, 10*time.Minute)
	models, err := client.FetchAvailableModels(context.Background(), "test-api-key", server.URL)
	if err != nil {
		t.Fatalf("FetchAvailableModels failed: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "anthropic/claude-3.7-sonnet" {
		t.Errorf("expected model ID anthropic/claude-3.7-sonnet, got %s", models[0].ID)
	}

	// Verify cached
	cached := client.GetCachedModels()
	if len(cached) != 2 {
		t.Fatalf("expected 2 cached models, got %d", len(cached))
	}
	if !client.IsCacheValid() {
		t.Errorf("expected cache to be valid")
	}
}

func TestFetchAvailableModels_AuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid API Key"}}`))
	}))
	defer server.Close()

	client := NewClient(5*time.Second, 10*time.Minute)
	_, err := client.FetchAvailableModels(context.Background(), "invalid-key", server.URL)
	if err == nil {
		t.Fatalf("expected error on unauthorized response, got nil")
	}
}

func TestCaching(t *testing.T) {
	client := NewClient(5*time.Second, 50*time.Millisecond)
	if client.IsCacheValid() {
		t.Errorf("empty cache should not be valid")
	}

	items := []ModelItem{
		{ID: "meta-llama/llama-3.3-70b-instruct", Name: "Llama 3.3 70B"},
	}
	client.SaveCache(items)

	if !client.IsCacheValid() {
		t.Errorf("cache should be valid immediately after SaveCache")
	}

	cached := client.GetCachedModels()
	if len(cached) != 1 || cached[0].ID != "meta-llama/llama-3.3-70b-instruct" {
		t.Errorf("unexpected cached items: %+v", cached)
	}

	// Wait for TTL expiration
	time.Sleep(60 * time.Millisecond)
	if client.IsCacheValid() {
		t.Errorf("cache should be invalid after TTL expired")
	}
}
