package format

import (
	"regexp"
)

const interleavedThinkingHint = "Interleaved thinking is enabled. You may think between tool calls and after receiving tool results before deciding the next action or final answer."

var invalidToolName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ConvertAnthropicToGoogle ports the Node proxy's Anthropic Messages to Google
// Generative AI conversion. The returned object is the inner request, before
// the Cloud Code project/model envelope is added.
func ConvertAnthropicToGoogle(request map[string]any, cache *SignatureCache) map[string]any {
	model := stringValue(request["model"])
	family := GetModelFamily(model)
	isThinking := IsThinkingModel(model)
	messages := cleanCacheControl(asSlice(request["messages"]))

	result := map[string]any{
		"contents":         []any{},
		"generationConfig": map[string]any{},
	}

	if system, exists := request["system"]; exists && system != nil {
		parts := make([]any, 0)
		if text, ok := system.(string); ok {
			parts = append(parts, map[string]any{"text": text})
		} else {
			for _, rawBlock := range asSlice(system) {
				block := asMap(rawBlock)
				if block != nil && block["type"] == "text" {
					parts = append(parts, map[string]any{"text": block["text"]})
				}
			}
		}
		if len(parts) > 0 {
			result["systemInstruction"] = map[string]any{"parts": parts}
		}
	}

	tools := asSlice(request["tools"])
	if family == FamilyClaude && isThinking && len(tools) > 0 {
		systemInstruction := asMap(result["systemInstruction"])
		if systemInstruction == nil {
			result["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": interleavedThinkingHint}}}
		} else {
			parts := asSlice(systemInstruction["parts"])
			var last map[string]any
			if len(parts) > 0 {
				last = asMap(parts[len(parts)-1])
			}
			if last != nil && stringValue(last["text"]) != "" {
				last["text"] = stringValue(last["text"]) + "\n\n" + interleavedThinkingHint
			} else {
				parts = append(parts, map[string]any{"text": interleavedThinkingHint})
			}
			systemInstruction["parts"] = parts
		}
	}

	processedMessages := messages
	if family == FamilyGemini && isThinking && needsThinkingRecovery(messages) {
		processedMessages = closeToolLoopForThinking(messages, FamilyGemini, cache)
	}
	if family == FamilyClaude && isThinking && (hasGeminiHistory(messages) || hasUnsignedThinkingBlocks(messages)) && needsThinkingRecovery(messages) {
		processedMessages = closeToolLoopForThinking(messages, FamilyClaude, cache)
	}

	contents := make([]any, 0, len(processedMessages))
	for _, rawMessage := range processedMessages {
		message := asMap(rawMessage)
		if message == nil {
			continue
		}
		content := message["content"]
		role := stringValue(message["role"])
		if (role == "assistant" || role == "model") && asSlice(content) != nil {
			blocks := restoreThinkingSignatures(asSlice(content))
			blocks = removeTrailingThinkingBlocks(blocks)
			content = reorderAssistantContent(blocks)
		}
		parts := convertContentToParts(content, family, cache)
		if len(parts) == 0 {
			parts = append(parts, map[string]any{"text": "."})
		}
		if family == FamilyClaude {
			parts = filterUnsignedThinkingParts(parts)
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"text": "."})
			}
		}
		contents = append(contents, map[string]any{"role": convertRole(role), "parts": parts})
	}
	result["contents"] = contents

	generation := asMap(result["generationConfig"])
	if value := intValue(request["max_tokens"], 0); value != 0 {
		generation["maxOutputTokens"] = value
	}
	copyIfPresent(request, generation, "temperature", "temperature")
	copyIfPresent(request, generation, "top_p", "topP")
	copyIfPresent(request, generation, "top_k", "topK")
	if stops := asSlice(request["stop_sequences"]); len(stops) > 0 {
		generation["stopSequences"] = cloneJSON(stops)
	}

	thinking := asMap(request["thinking"])
	if isThinking && family == FamilyClaude {
		budget := intValue(thinking["budget_tokens"], DefaultClaudeThinkBudget)
		if budget == 0 {
			budget = DefaultClaudeThinkBudget
		}
		generation["thinkingConfig"] = map[string]any{"include_thoughts": true, "thinking_budget": budget}
		maximum := intValue(generation["maxOutputTokens"], 0)
		if maximum > 0 && maximum <= budget {
			generation["maxOutputTokens"] = budget + 8192
		}
	} else if isThinking && family == FamilyGemini {
		generation["thinkingConfig"] = map[string]any{
			"includeThoughts": true,
			"thinkingBudget":  clampGeminiThinkingBudget(model, thinking["budget_tokens"]),
		}
	}

	if len(tools) > 0 {
		declarations := make([]any, 0, len(tools))
		for index, rawTool := range tools {
			tool := asMap(rawTool)
			function := asMap(tool["function"])
			custom := asMap(tool["custom"])
			name := firstNonEmpty(tool["name"], function["name"], custom["name"])
			if name == "" {
				name = "tool-" + stringValue(index)
			}
			name = invalidToolName.ReplaceAllString(name, "_")
			if len(name) > 64 {
				name = name[:64]
			}
			description := firstNonEmpty(tool["description"], function["description"], custom["description"])
			schema := firstValue(
				tool["input_schema"], function["input_schema"], function["parameters"],
				custom["input_schema"], tool["parameters"],
			)
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			parameters := CleanSchema(SanitizeSchema(schema))
			declarations = append(declarations, map[string]any{
				"name": name, "description": description, "parameters": parameters,
			})
		}
		result["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
		if family == FamilyClaude {
			result["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "VALIDATED"}}
		}
	}

	if family == FamilyGemini && intValue(generation["maxOutputTokens"], 0) > GeminiMaxOutputTokens {
		generation["maxOutputTokens"] = GeminiMaxOutputTokens
	}
	return result
}

func copyIfPresent(source, destination map[string]any, sourceKey, destinationKey string) {
	if value, exists := source[sourceKey]; exists {
		destination[destinationKey] = value
	}
}

func firstNonEmpty(values ...any) string {
	for _, value := range values {
		if text := stringValue(value); text != "" {
			return text
		}
	}
	return ""
}

func firstValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
