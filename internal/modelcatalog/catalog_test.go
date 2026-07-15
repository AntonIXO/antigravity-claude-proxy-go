package modelcatalog

import "testing"

func TestParseUsesAgyAgentModelOrderAndResolvesRoutingAlias(t *testing.T) {
	t.Parallel()
	catalog, err := Parse([]byte(`{
		"defaultAgentModelId":"gemini-3.5-flash-low",
		"agentModelSorts":[{"displayName":"Recommended","groups":[{"modelIds":[
			"gemini-3.5-flash-low","gemini-3-flash-agent","gemini-pro-agent","claude-opus-4-6-thinking","gpt-oss-120b-medium"
		]}]}],
		"models":{
			"gemini-3.5-flash-low":{"displayName":"Gemini 3.5 Flash (Medium)","supportsThinking":true,"thinkingBudget":4000,"maxTokens":1048576,"maxOutputTokens":65536},
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
