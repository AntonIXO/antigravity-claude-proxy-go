package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
	proxyformat "antigravity-go-proxy/internal/format"
)

func main() {
	project := flag.String("project", "", "Cloud Code project ID")
	model := flag.String("model", "claude-sonnet-4-6", "Cloud Code model ID")
	timeout := flag.Duration("timeout", 90*time.Second, "end-to-end gate timeout")
	flag.Parse()
	if *project == "" {
		log.Fatal("-project is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	credentials, err := (auth.Manager{}).Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	builder := proxyformat.NewBuilder()
	request := map[string]any{
		"model": *model,
		"messages": []any{map[string]any{
			"role": "user", "content": "Reply with exactly FORMAT_GATE_OK",
		}},
		"max_tokens": 2048,
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": 1024},
	}
	payload := builder.BuildCloudCodeRequest(request, *project, credentials.Email)
	innerRequest, _ := payload["request"].(map[string]any)
	sessionID, _ := innerRequest["sessionId"].(string)

	client := cloudcode.New(cloudcode.Options{AccessToken: credentials.AccessToken, Timeout: *timeout})
	defer client.CloseIdleConnections()
	stream := proxyformat.NewStreamConverter(*model, builder.Cache, "msg_format_gate")
	accumulator := proxyformat.NewThinkingAccumulator()
	eventCount := 0
	headers := make(http.Header)
	if proxyformat.GetModelFamily(*model) == proxyformat.FamilyClaude && proxyformat.IsThinkingModel(*model) {
		headers.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
	}
	response, err := client.StreamGenerateContent(ctx, payload, cloudcode.RequestOptions{
		SessionID: sessionID,
		Headers:   headers,
	}, func(event cloudcode.SSEEvent) error {
		if err := accumulator.Consume(event.Data); err != nil {
			return err
		}
		converted, err := stream.Consume(event.Data)
		if err != nil {
			return err
		}
		eventCount += len(converted)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	finalEvents, err := stream.Finish()
	if err != nil {
		log.Fatal(err)
	}
	eventCount += len(finalEvents)
	converted := accumulator.Response(*model, builder.Cache, "msg_format_gate")
	content, _ := converted["content"].([]any)
	if len(content) == 0 {
		log.Fatal("format gate returned no Anthropic content blocks")
	}
	fmt.Printf("format_ok endpoint=%s status=%d model=%s blocks=%d anthropic_events=%d stop_reason=%v\n",
		response.Endpoint, response.StatusCode, *model, len(content), eventCount, converted["stop_reason"])
}
