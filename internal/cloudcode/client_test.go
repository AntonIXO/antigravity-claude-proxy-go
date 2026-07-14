package cloudcode

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTransportKeepsTLSZeroValueAndHTTP2Disabled(t *testing.T) {
	t.Parallel()
	client := New(Options{})
	if client.transport == nil {
		t.Fatal("dedicated transport was not created")
	}
	if !reflect.DeepEqual(client.transport.TLSClientConfig, &tls.Config{}) {
		t.Fatalf("TLS config is not empty: %#v", client.transport.TLSClientConfig)
	}
	if client.transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 must remain false for current-agy ALPN")
	}
	if client.transport.TLSNextProto != nil {
		t.Fatalf("TLSNextProto must remain nil: %#v", client.transport.TLSNextProto)
	}
}

func TestFetchAvailableModelsHeadersAndDailyFallback(t *testing.T) {
	t.Parallel()
	var dailyCalls, prodCalls int
	daily := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dailyCalls++
		if request.URL.Path != PathFetchAvailableModels {
			t.Errorf("path = %q", request.URL.Path)
		}
		assertHeader(t, request, "Authorization", "Bearer token")
		assertHeader(t, request, "X-Client-Name", "antigravity")
		assertHeader(t, request, "X-Client-Version", DefaultClientVersion)
		assertHeader(t, request, "x-goog-api-client", GoogleAPIClient)
		assertHeader(t, request, "Accept-Encoding", "identity")
		if !strings.HasPrefix(request.Header.Get("User-Agent"), "antigravity/2.0.3 ") {
			t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["project"] != "project" {
			t.Errorf("body = %#v", body)
		}
		http.Error(writer, "capacity", http.StatusServiceUnavailable)
	}))
	defer daily.Close()
	prod := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		prodCalls++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"models":{}}`))
	}))
	defer prod.Close()

	client := New(Options{AccessToken: "token", HTTPClient: daily.Client()})
	client.contentEndpoints = []string{daily.URL, prod.URL}
	response, err := client.FetchAvailableModels(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if dailyCalls != 1 || prodCalls != 1 || response.Endpoint != prod.URL {
		t.Fatalf("daily=%d prod=%d endpoint=%q", dailyCalls, prodCalls, response.Endpoint)
	}
}

func TestLoadCodeAssistMetadata(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body LoadCodeAssistRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Mode != 1 || body.Metadata.IdeType != 9 || body.Metadata.Platform != 3 || body.Metadata.PluginType != 2 || body.Metadata.DuetProject != "project" {
			t.Errorf("body = %#v", body)
		}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := New(Options{AccessToken: "token", HTTPClient: server.Client()})
	client.provisioningEndpoints = []string{server.URL}
	if _, err := client.LoadCodeAssist(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
}

func TestParseSSE(t *testing.T) {
	t.Parallel()
	input := ": keepalive\r\nid: one\r\nevent: message\r\nretry: 1500\r\ndata: {\"a\":\r\ndata: 1}\r\n\r\ndata: [DONE]"
	var events []SSEEvent
	err := ParseSSE(strings.NewReader(input), func(event SSEEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Event != "message" || string(events[0].Data) != "{\"a\":\n1}" || events[0].ID != "one" || events[0].Retry != 1500*time.Millisecond {
		t.Fatalf("first event = %#v", events[0])
	}
	if string(events[1].Data) != "[DONE]" || events[1].ID != "one" {
		t.Fatalf("second event = %#v", events[1])
	}
}

func assertHeader(t *testing.T, request *http.Request, name, want string) {
	t.Helper()
	if got := request.Header.Get(name); got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}
