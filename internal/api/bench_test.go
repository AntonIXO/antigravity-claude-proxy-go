package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
	proxyformat "antigravity-go-proxy/internal/format"
)

type mockStreamSender struct {
	events []cloudcode.SSEEvent
}

func (m *mockStreamSender) StreamGenerateContent(ctx context.Context, payload any, options cloudcode.RequestOptions, consume func(cloudcode.SSEEvent) error) (cloudcode.Response, error) {
	for _, event := range m.events {
		if err := consume(event); err != nil {
			return cloudcode.Response{}, err
		}
	}
	return cloudcode.Response{StatusCode: 200}, nil
}

func (m *mockStreamSender) LoadCodeAssist(context.Context, string) (cloudcode.Response, error) {
	return cloudcode.Response{}, nil
}

func (m *mockStreamSender) FetchAvailableModels(context.Context, string) (cloudcode.Response, error) {
	return cloudcode.Response{}, nil
}

func BenchmarkStreamMessage(b *testing.B) {
	payload1 := `{"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"I should ","thoughtSignature":"claude-signature-0123456789012345678901234567890123456789"}]}}],"usageMetadata":{"promptTokenCount":120,"cachedContentTokenCount":20}}}`
	payload2 := `{"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"inspect.","thoughtSignature":"claude-signature-0123456789012345678901234567890123456789"},{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":9,"cachedContentTokenCount":20}}}`
	
	sender := &mockStreamSender{
		events: []cloudcode.SSEEvent{
			{Event: "message", Data: []byte(payload1)},
			{Event: "message", Data: []byte(payload2)},
		},
	}
	
	server, err := New(Options{
		APIKey: "test",
		Credentials: func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{}, nil
		},
		NewUpstream: func(string) Upstream {
			return sender
		},
		Builder: proxyformat.NewBuilder(),
	})
	if err != nil {
		b.Fatal(err)
	}
	
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		server.streamMessage(w, req, func(ctx context.Context, consume func(cloudcode.SSEEvent) error) (cloudcode.Response, error) {
			return sender.StreamGenerateContent(ctx, nil, cloudcode.RequestOptions{}, consume)
		}, "claude-sonnet-4-6-thinking")
	}
}
