package modelcatalog

import (
	"strings"
	"testing"
)

func TestParseUsesAgyAgentModelOrderAndResolvesRoutingAlias(t *testing.T) {
	t.Parallel()
	catalog, err := Parse([]byte(`{
		"defaultAgentModelId":"gemini-3.5-flash-low",
		"agentModelSorts":[{"displayName":"Recommended","groups":[{"modelIds":[
			"gemini-3.5-flash-low","gemini-3-flash-agent","gemini-pro-agent","claude-opus-4-6-thinking","gpt-oss-120b-medium"
		]}]}],
		"models":{
			"gemini-3.5-flash-low":{"displayName":"Gemini 3.5 Flash (Medium)","supportsThinking":true,"thinkingBudget":4000,"maxTokens":1048576,"maxOutputTokens":65536,"quotaInfo":{"remainingFraction":0.75,"resetTime":"2026-07-15T06:26:33Z"}},
			"gemini-3-flash-agent":{"displayName":"Gemini 3.5 Flash (High)","supportsThinking":true,"thinkingBudget":10000,"maxTokens":1048576,"maxOutputTokens":65536},
			"gemini-3.1-pro-high":{"displayName":"Gemini 3.1 Pro (High)","supportsThinking":true,"thinkingBudget":10001,"maxOutputTokens":65535},
			"gemini-pro-agent":{"displayName":"Gemini 3.1 Pro (High)","supportsThinking":true,"thinkingBudget":10001,"maxTokens":1048576,"maxOutputTokens":65535},
			"claude-opus-4-6-thinking":{"displayName":"Claude Opus 4.6 (Thinking)","supportsThinking":true,"thinkingBudget":1024,"maxTokens":250000,"maxOutputTokens":64000},
			"gpt-oss-120b-medium":{"displayName":"GPT-OSS 120B (Medium)","supportsThinking":true,"thinkingBudget":8192,"maxTokens":131072,"maxOutputTokens":32768},
			"gemini-3.1-flash-image":{"displayName":"Gemini 3.1 Flash Image"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gemini-3.5-flash-low", "gemini-3-flash-agent", "gemini-pro-agent", "claude-opus-4-6-thinking", "gpt-oss-120b-medium"}
	models := catalog.Selectable()
	if len(models) != len(want) {
		t.Fatalf("selectable models=%#v", models)
	}
	for index, id := range want {
		if models[index].ID != id {
			t.Fatalf("model %d=%q, want %q", index, models[index].ID, id)
		}
	}
	if models[0].QuotaRemainingFraction == nil || *models[0].QuotaRemainingFraction != 0.75 || models[0].QuotaResetTime != "2026-07-15T06:26:33Z" {
		t.Fatalf("model quota=%#v", models[0])
	}
	resolved, err := catalog.Resolve("gemini-3.1-pro-high")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != "gemini-pro-agent" || resolved.ThinkingBudget != 10001 || resolved.MaxOutputTokens != 65535 {
		t.Fatalf("resolved alias=%#v", resolved)
	}
	if _, err := catalog.Resolve("gemini-3.1-flash-image"); err == nil {
		t.Fatal("image-only model was accepted as an agent model")
	}
}

func TestParseHandlesExhaustedQuotaWithNullRemainingFraction(t *testing.T) {
	t.Parallel()
	catalog, err := Parse([]byte(`{
		"defaultAgentModelId":"gemini-3.5-flash-low",
		"agentModelSorts":[{"displayName":"Recommended","groups":[{"modelIds":["gemini-3.5-flash-low"]}]}],
		"models":{
			"gemini-3.5-flash-low":{"displayName":"Gemini 3.5 Flash (Low)","quotaInfo":{"resetTime":"2026-08-14T12:00:00Z"}}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	models := catalog.Selectable()
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].QuotaRemainingFraction == nil {
		t.Fatal("expected non-nil QuotaRemainingFraction for exhausted quota")
	}
	if *models[0].QuotaRemainingFraction != 0.0 {
		t.Fatalf("expected 0.0 remaining fraction, got %f", *models[0].QuotaRemainingFraction)
	}
	if models[0].QuotaResetTime != "2026-08-14T12:00:00Z" {
		t.Fatalf("unexpected reset time: %s", models[0].QuotaResetTime)
	}
}

func TestSynthetic37AndReasoningResolution(t *testing.T) {
	t.Parallel()
	catalog, err := Parse([]byte(`{
		"defaultAgentModelId":"gemini-3.6-flash-high",
		"agentModelSorts":[{"displayName":"Recommended","groups":[{"modelIds":[
			"gemini-3.6-flash-high","gemini-3.6-flash-medium","gemini-3.6-flash-low"
		]}]}],
		"models":{
			"gemini-3.6-flash-high":{"displayName":"Gemini 3.6 Flash (High)","supportsThinking":true,"thinkingBudget":16000},
			"gemini-3.6-flash-medium":{"displayName":"Gemini 3.6 Flash (Medium)","supportsThinking":true,"thinkingBudget":8000},
			"gemini-3.6-flash-low":{"displayName":"Gemini 3.6 Flash (Low)","supportsThinking":true,"thinkingBudget":1024}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	// 1. Verify synthetic gemini-3.7-flash-high has UpstreamID pointing to gemini-3.6-flash-high
	m37High, err := catalog.Resolve("gemini-3.7-flash-high")
	if err != nil {
		t.Fatal(err)
	}
	if m37High.ID != "gemini-3.7-flash-high" || m37High.GetUpstreamID() != "gemini-3.6-flash-high" {
		t.Fatalf("expected ID=gemini-3.7-flash-high UpstreamID=gemini-3.6-flash-high, got ID=%q UpstreamID=%q", m37High.ID, m37High.GetUpstreamID())
	}

	// 2. Base model gemini-3.7-flash resolves to gemini-3.7-flash-high by default
	base37, err := catalog.ResolveWithRequest("gemini-3.7-flash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if base37.ID != "gemini-3.7-flash-high" || base37.GetUpstreamID() != "gemini-3.6-flash-high" {
		t.Fatalf("base model default resolution failed: ID=%q UpstreamID=%q", base37.ID, base37.GetUpstreamID())
	}

	// 3. reasoning_effort="medium" resolves gemini-3.7-flash to gemini-3.7-flash-medium
	med37, err := catalog.ResolveWithRequest("gemini-3.7-flash", map[string]any{"reasoning_effort": "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if med37.ID != "gemini-3.7-flash-medium" || med37.GetUpstreamID() != "gemini-3.6-flash-medium" {
		t.Fatalf("reasoning_effort medium failed: ID=%q UpstreamID=%q", med37.ID, med37.GetUpstreamID())
	}

	// 4. thinking={"type": "disabled"} cannot turn 3.7 thinking off — clamp to low.
	dis37, err := catalog.ResolveWithRequest("gemini-3.7-flash", map[string]any{"thinking": map[string]any{"type": "disabled"}})
	if err != nil {
		t.Fatal(err)
	}
	if dis37.ID != "gemini-3.7-flash-low" || !dis37.SupportsThinking {
		t.Fatalf("disabled 3.7 should clamp to low with thinking on, got ID=%q SupportsThinking=%v", dis37.ID, dis37.SupportsThinking)
	}

	// 5. reasoning_effort="xhigh" or "max" maps to "high"
	xhigh37, err := catalog.ResolveWithRequest("gemini-3.7-flash", map[string]any{"reasoning_effort": "xhigh"})
	if err != nil {
		t.Fatal(err)
	}
	if xhigh37.ID != "gemini-3.7-flash-high" || xhigh37.GetUpstreamID() != "gemini-3.6-flash-high" {
		t.Fatalf("reasoning_effort xhigh mapping failed: ID=%q UpstreamID=%q", xhigh37.ID, xhigh37.GetUpstreamID())
	}

	max37, err := catalog.ResolveWithRequest("gemini-3.7-flash", map[string]any{"reasoning_effort": "max"})
	if err != nil {
		t.Fatal(err)
	}
	if max37.ID != "gemini-3.7-flash-high" || max37.GetUpstreamID() != "gemini-3.6-flash-high" {
		t.Fatalf("reasoning_effort max mapping failed: ID=%q UpstreamID=%q", max37.ID, max37.GetUpstreamID())
	}
}

func TestGemini37UsesTieredUpstreamWhenPresent(t *testing.T) {
	t.Parallel()
	catalog, err := Parse([]byte(`{
		"defaultAgentModelId":"gemini-3.6-flash-high",
		"agentModelSorts":[{"displayName":"Recommended","groups":[{"modelIds":[
			"gemini-3.6-flash-high","gemini-3.6-flash-medium","gemini-3.6-flash-low"
		]}]}],
		"models":{
			"gemini-3.6-flash-high":{"displayName":"Gemini 3.6 Flash (High)","supportsThinking":true,"thinkingBudget":-1},
			"gemini-3.6-flash-medium":{"displayName":"Gemini 3.6 Flash (Medium)","supportsThinking":true,"thinkingBudget":4000},
			"gemini-3.6-flash-low":{"displayName":"Gemini 3.6 Flash (Low)","supportsThinking":true,"thinkingBudget":1000},
			"gemini-3.7-flash-tiered":{"supportsThinking":true,"thinkingBudget":-1,"minThinkingBudget":32,"maxTokens":1048576,"maxOutputTokens":65536}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	high, err := catalog.Resolve("gemini-3.7-flash-high")
	if err != nil {
		t.Fatal(err)
	}
	if high.ID != "gemini-3.7-flash-high" || high.GetUpstreamID() != "gemini-3.7-flash-tiered" || high.ThinkingLevel != "HIGH" {
		t.Fatalf("3.7 high: ID=%q UpstreamID=%q ThinkingLevel=%q", high.ID, high.GetUpstreamID(), high.ThinkingLevel)
	}

	base, err := catalog.ResolveWithRequest("gemini-3.7-flash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if base.ID != "gemini-3.7-flash-high" || base.GetUpstreamID() != "gemini-3.7-flash-tiered" || base.ThinkingLevel != "HIGH" {
		t.Fatalf("3.7 default: ID=%q UpstreamID=%q ThinkingLevel=%q", base.ID, base.GetUpstreamID(), base.ThinkingLevel)
	}

	med, err := catalog.ResolveWithRequest("gemini-3.7-flash", map[string]any{"reasoning_effort": "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if med.ID != "gemini-3.7-flash-medium" || med.GetUpstreamID() != "gemini-3.7-flash-tiered" || med.ThinkingLevel != "MEDIUM" {
		t.Fatalf("3.7 medium: ID=%q UpstreamID=%q ThinkingLevel=%q", med.ID, med.GetUpstreamID(), med.ThinkingLevel)
	}

	low, err := catalog.ResolveWithRequest("gemini-3.7-flash", map[string]any{"thinking": map[string]any{"type": "disabled"}})
	if err != nil {
		t.Fatal(err)
	}
	if low.ID != "gemini-3.7-flash-low" || low.GetUpstreamID() != "gemini-3.7-flash-tiered" || low.ThinkingLevel != "LOW" || !low.SupportsThinking {
		t.Fatalf("3.7 disabled→low: %#v", low)
	}
	for _, m := range catalog.PublicModels() {
		if strings.Contains(m.ID, "tiered") || strings.Contains(m.DisplayName, "tiered") {
			t.Fatalf("public catalog leaked tiered id: %#v", m)
		}
	}
	for _, m := range catalog.Selectable() {
		if strings.Contains(m.ID, "tiered") {
			t.Fatalf("selectable id leaked tiered: %q", m.ID)
		}
	}
}
