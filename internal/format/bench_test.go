package format

import (
	"encoding/json"
	"os"
	"testing"
)

func BenchmarkConvertAnthropicToGoogle(b *testing.B) {
	request := readBenchObjectFixture(b, "testdata/anthropic-request.json")
	cache := NewSignatureCache()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ConvertAnthropicToGoogle(request, cache)
	}
}

func BenchmarkConvertGoogleToAnthropic(b *testing.B) {
	response := readBenchObjectFixture(b, "testdata/google-response.json")
	cache := NewSignatureCache()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ConvertGoogleToAnthropicWithID(response, "claude-sonnet-4-6-thinking", cache, "msg_bench")
	}
}

func BenchmarkStreamConverterConsume(b *testing.B) {
	payload1 := `{"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"I should ","thoughtSignature":"claude-signature-0123456789012345678901234567890123456789"}]}}],"usageMetadata":{"promptTokenCount":120,"cachedContentTokenCount":20}}}`
	payload2 := `{"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"inspect.","thoughtSignature":"claude-signature-0123456789012345678901234567890123456789"},{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":9,"cachedContentTokenCount":20}}}`
	
	cache := NewSignatureCache()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stream := NewStreamConverter("claude-sonnet-4-6-thinking", cache, "msg_bench")
		stream.Consume([]byte(payload1))
		stream.Consume([]byte(payload2))
		stream.Finish()
	}
}

func BenchmarkThinkingAccumulator(b *testing.B) {
	payload1 := `{"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"I should ","thoughtSignature":"claude-signature-0123456789012345678901234567890123456789"}]}}],"usageMetadata":{"promptTokenCount":120,"cachedContentTokenCount":20}}}`
	payload2 := `{"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"inspect.","thoughtSignature":"claude-signature-0123456789012345678901234567890123456789"},{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":9,"cachedContentTokenCount":20}}}`
	
	cache := NewSignatureCache()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		acc := NewThinkingAccumulator()
		acc.Consume([]byte(payload1))
		acc.Consume([]byte(payload2))
		acc.Response("claude-sonnet-4-6-thinking", cache, "msg_bench")
	}
}

func BenchmarkSanitizeSchema(b *testing.B) {
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
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SanitizeSchema(schema)
	}
}

func BenchmarkCleanSchema(b *testing.B) {
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
	sanitized := SanitizeSchema(schema)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CleanSchema(sanitized)
	}
}

func readBenchObjectFixture(b *testing.B, path string) map[string]any {
	b.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(contents, &result); err != nil {
		b.Fatal(err)
	}
	return result
}
