package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"antigravity-go-proxy/internal/cloudcode"
)

func TestRequestConversionMatchesNodeFixture(t *testing.T) {
	t.Parallel()
	request := readObjectFixture(t, "testdata/anthropic-request.json")
	want := readObjectFixture(t, "testdata/google-request.json")
	got := ConvertAnthropicToGoogle(request, NewSignatureCache())
	assertJSONEqual(t, got, want)

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("cache_control")) {
		t.Fatalf("cache_control leaked into Cloud Code request: %s", encoded)
	}
}

func TestResponseConversionMatchesNodeFixture(t *testing.T) {
	t.Parallel()
	cache := NewSignatureCache()
	response := readObjectFixture(t, "testdata/google-response.json")
	want := readObjectFixture(t, "testdata/anthropic-response.json")
	got := ConvertGoogleToAnthropicWithID(response, "claude-sonnet-4-6-thinking", cache, "msg_fixture")
	assertJSONEqual(t, got, want)

	if family := cache.ThinkingFamily("claude-signature-0123456789012345678901234567890123456789"); family != FamilyClaude {
		t.Fatalf("thinking signature family = %q", family)
	}
	if signature := cache.Tool("tool-1"); signature != "tool-signature-012345678901234567890123456789012345678901" {
		t.Fatalf("cached tool signature = %q", signature)
	}
}

func TestCloudCodeSSEStreamingAndNonStreamingRoundTrip(t *testing.T) {
	t.Parallel()
	input, err := os.Open("testdata/cloudcode-stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	cache := NewSignatureCache()
	stream := NewStreamConverter("claude-sonnet-4-6-thinking", cache, "msg_stream")
	accumulator := NewThinkingAccumulator()
	var events []map[string]any
	err = cloudcode.ParseSSE(input, func(event cloudcode.SSEEvent) error {
		converted, convertErr := stream.Consume(event.Data)
		if convertErr != nil {
			return convertErr
		}
		events = append(events, converted...)
		return accumulator.Consume(event.Data)
	})
	if err != nil {
		t.Fatal(err)
	}
	finalEvents, err := stream.Finish()
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, finalEvents...)
	var wantEvents []any
	streamFixture, err := os.ReadFile("testdata/anthropic-stream-events.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(streamFixture, &wantEvents); err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, events, wantEvents)

	wantTypes := []string{
		"message_start", "content_block_start", "content_block_delta",
		"content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	gotTypes := make([]string, 0, len(events))
	for _, event := range events {
		gotTypes = append(gotTypes, stringValue(event["type"]))
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event types = %#v, want %#v\nevents: %#v", gotTypes, wantTypes, events)
	}
	if delta := asMap(events[4]["delta"]); delta["type"] != "signature_delta" || delta["signature"] != "claude-signature-0123456789012345678901234567890123456789" {
		t.Fatalf("signature event = %#v", events[4])
	}
	messageDelta := events[len(events)-2]
	if asMap(messageDelta["delta"])["stop_reason"] != "end_turn" || asMap(messageDelta["usage"])["output_tokens"] != 9 {
		t.Fatalf("message delta = %#v", messageDelta)
	}

	nonStreaming := accumulator.Response("claude-sonnet-4-6-thinking", cache, "msg_accumulated")
	want := map[string]any{
		"id": "msg_accumulated", "type": "message", "role": "assistant",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "I should inspect.", "signature": "claude-signature-0123456789012345678901234567890123456789"},
			map[string]any{"type": "text", "text": "Done."},
		},
		"model": "claude-sonnet-4-6-thinking", "stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens": 100, "output_tokens": 9,
			"cache_read_input_tokens": 20, "cache_creation_input_tokens": 0,
		},
	}
	assertJSONEqual(t, nonStreaming, want)
}

func TestStreamRejectsEmptyResponse(t *testing.T) {
	t.Parallel()
	stream := NewStreamConverter("gemini-3.5-flash", NewSignatureCache(), "msg_empty")
	if _, err := stream.Consume([]byte(`{"response":{"candidates":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Finish(); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("Finish error = %v", err)
	}
}

func TestBuilderUsesStablePerAccountSessionAndExactEnvelope(t *testing.T) {
	t.Parallel()
	request := map[string]any{
		"model":    "gemini-3.5-flash-low",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	sessions := NewSessionStore()
	sequence := 0
	sessions.newID = func() string {
		sequence++
		return "session-" + stringValue(sequence)
	}
	builder := &Builder{Cache: NewSignatureCache(), Sessions: sessions, NewRequestID: func() string { return "request-id" }}
	first := builder.BuildCloudCodeRequest(request, "project", "user@example.com")
	second := builder.BuildCloudCodeRequest(request, "project", "user@example.com")
	if first["requestId"] != "agent-request-id" || first["requestType"] != "agent" || first["userAgent"] != "antigravity" {
		t.Fatalf("envelope = %#v", first)
	}
	firstRequest := asMap(first["request"])
	secondRequest := asMap(second["request"])
	if firstRequest["sessionId"] != "session-1" || secondRequest["sessionId"] != "session-1" {
		t.Fatalf("sessions = %q, %q", firstRequest["sessionId"], secondRequest["sessionId"])
	}
	parts := asSlice(asMap(firstRequest["systemInstruction"])["parts"])
	if len(parts) != 2 || asMap(parts[0])["text"] != AntigravitySystemInstruction {
		t.Fatalf("system instruction = %#v", firstRequest["systemInstruction"])
	}
}

func TestGeminiToolSignatureRestorationAndBudgetClamp(t *testing.T) {
	t.Parallel()
	cache := NewSignatureCache()
	signature := strings.Repeat("s", MinSignatureLength)
	cache.CacheTool("tool-1", signature)
	request := map[string]any{
		"model": "gemini-2.5-flash-thinking",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "tool_use", "id": "tool-1", "name": "read", "input": map[string]any{}},
				},
			},
		},
		"max_tokens": 100000,
		"thinking":   map[string]any{"budget_tokens": 100000},
	}
	converted := ConvertAnthropicToGoogle(request, cache)
	part := asMap(asSlice(asMap(asSlice(converted["contents"])[0])["parts"])[0])
	if part["thoughtSignature"] != signature {
		t.Fatalf("thoughtSignature = %q", part["thoughtSignature"])
	}
	generation := asMap(converted["generationConfig"])
	if generation["maxOutputTokens"] != GeminiMaxOutputTokens {
		t.Fatalf("maxOutputTokens = %v", generation["maxOutputTokens"])
	}
	if asMap(generation["thinkingConfig"])["thinkingBudget"] != 24576 {
		t.Fatalf("thinkingConfig = %#v", generation["thinkingConfig"])
	}
}

func TestSignatureCacheExpires(t *testing.T) {
	t.Parallel()
	cache := NewSignatureCache()
	now := time.Unix(100, 0)
	cache.now = func() time.Time { return now }
	cache.CacheTool("tool", "signature")
	now = now.Add(signatureCacheTTL + time.Millisecond)
	if got := cache.Tool("tool"); got != "" {
		t.Fatalf("expired signature = %q", got)
	}
}

func TestGeminiToolLoopWithoutThinkingGetsRecoveryTurn(t *testing.T) {
	t.Parallel()
	request := map[string]any{
		"model": "gemini-3.5-flash-low",
		"messages": []any{
			map[string]any{"role": "user", "content": "use a tool"},
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "tool-1", "name": "read", "input": map[string]any{},
					"thoughtSignature": strings.Repeat("s", MinSignatureLength),
				}},
			},
			map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": "tool-1", "content": "done",
				}},
			},
		},
	}
	converted := ConvertAnthropicToGoogle(request, NewSignatureCache())
	contents := asSlice(converted["contents"])
	if len(contents) != 5 {
		t.Fatalf("contents = %#v", contents)
	}
	if asMap(asSlice(asMap(contents[3])["parts"])[0])["text"] != "[Tool execution completed.]" {
		t.Fatalf("synthetic assistant = %#v", contents[3])
	}
	if asMap(asSlice(asMap(contents[4])["parts"])[0])["text"] != "[Continue]" {
		t.Fatalf("synthetic user = %#v", contents[4])
	}
}

func TestSchemaCleanerHandlesNestedArraysUnionsAndNullable(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"todos": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title": map[string]any{"type": "string"},
						"done":  map[string]any{"type": "boolean"},
					},
				},
			},
			"optional": map[string]any{"type": []any{"string", "null"}},
		},
		"required": []any{"todos", "optional", "missing"},
	}
	cleaned := asMap(CleanSchema(SanitizeSchema(schema)))
	properties := asMap(cleaned["properties"])
	items := asMap(asMap(properties["todos"])["items"])
	if cleaned["type"] != "OBJECT" || asMap(properties["todos"])["type"] != "ARRAY" || items["type"] != "OBJECT" {
		t.Fatalf("nested types = %#v", cleaned)
	}
	if asMap(asMap(items["properties"])["done"])["type"] != "BOOLEAN" {
		t.Fatalf("nested primitive = %#v", cleaned)
	}
	if asMap(properties["optional"])["type"] != "STRING" || !reflect.DeepEqual(cleaned["required"], []any{"todos"}) {
		t.Fatalf("nullable/required = %#v", cleaned)
	}

	union := map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
		},
	}
	cleanedUnion := asMap(CleanSchema(union))
	if cleanedUnion["type"] != "OBJECT" || asMap(asMap(cleanedUnion["properties"])["name"])["type"] != "STRING" {
		t.Fatalf("cleaned union = %#v", cleanedUnion)
	}
}

func readObjectFixture(t *testing.T, path string) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(contents, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}
