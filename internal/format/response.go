package format

// ConvertGoogleToAnthropic converts the inner Cloud Code response (or its
// {response: ...} wrapper) into an Anthropic Messages response.
func ConvertGoogleToAnthropic(googleResponse map[string]any, model string, cache *SignatureCache) map[string]any {
	return ConvertGoogleToAnthropicWithID(googleResponse, model, cache, "msg_"+randomHex(16))
}

func ConvertGoogleToAnthropicWithID(googleResponse map[string]any, model string, cache *SignatureCache, messageID string) map[string]any {
	if messageID == "" {
		messageID = "msg_" + randomHex(16)
	}
	response := asMap(googleResponse["response"])
	if response == nil {
		response = googleResponse
	}
	candidates := asSlice(response["candidates"])
	var candidate map[string]any
	if len(candidates) > 0 {
		candidate = asMap(candidates[0])
	}
	content := asMap(candidate["content"])
	parts := asSlice(content["parts"])
	blocks := make([]any, 0, len(parts))
	hasTools := false
	for _, rawPart := range parts {
		part := asMap(rawPart)
		if part == nil {
			continue
		}
		if text, exists := part["text"]; exists {
			if part["thought"] == true {
				signature := stringValue(part["thoughtSignature"])
				if len(signature) >= MinSignatureLength {
					cache.CacheThinking(signature, GetModelFamily(model))
				}
				blocks = append(blocks, map[string]any{"type": "thinking", "thinking": text, "signature": signature})
			} else {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			continue
		}
		if call := asMap(part["functionCall"]); call != nil {
			toolID := stringValue(call["id"])
			if toolID == "" {
				toolID = "toolu_" + randomHex(12)
			}
			block := map[string]any{
				"type": "tool_use", "id": toolID, "name": call["name"], "input": mapOrEmpty(call["args"]),
			}
			signature := stringValue(part["thoughtSignature"])
			if len(signature) >= MinSignatureLength {
				block["thoughtSignature"] = signature
				cache.CacheTool(toolID, signature)
			}
			blocks = append(blocks, block)
			hasTools = true
			continue
		}
		if inline := asMap(part["inlineData"]); inline != nil {
			blocks = append(blocks, map[string]any{
				"type":   "image",
				"source": map[string]any{"type": "base64", "media_type": inline["mimeType"], "data": inline["data"]},
			})
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": ""})
	}

	stopReason := "end_turn"
	switch stringValue(candidate["finishReason"]) {
	case "STOP":
		stopReason = "end_turn"
	case "MAX_TOKENS":
		stopReason = "max_tokens"
	case "TOOL_USE":
		stopReason = "tool_use"
	default:
		if hasTools {
			stopReason = "tool_use"
		}
	}
	usage := asMap(response["usageMetadata"])
	promptTokens := intValue(usage["promptTokenCount"], 0)
	cachedTokens := intValue(usage["cachedContentTokenCount"], 0)
	return map[string]any{
		"id": messageID, "type": "message", "role": "assistant", "content": blocks,
		"model": model, "stop_reason": stopReason, "stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":                promptTokens - cachedTokens,
			"output_tokens":               intValue(usage["candidatesTokenCount"], 0),
			"cache_read_input_tokens":     cachedTokens,
			"cache_creation_input_tokens": 0,
		},
	}
}

// ThinkingAccumulator combines Cloud Code SSE deltas into the single inner
// response expected by the non-streaming Anthropic endpoint.
type ThinkingAccumulator struct {
	thinkingText      string
	thinkingSignature string
	text              string
	parts             []any
	usage             map[string]any
	finishReason      string
}

func NewThinkingAccumulator() *ThinkingAccumulator {
	return &ThinkingAccumulator{finishReason: "STOP", usage: map[string]any{}}
}

func (accumulator *ThinkingAccumulator) Consume(data []byte) error {
	inner, err := decodeCloudCodeEvent(data)
	if err != nil || inner == nil {
		// Skip malformed data frames rather than failing an otherwise healthy
		// response stream.
		return nil
	}
	if usage := asMap(inner["usageMetadata"]); usage != nil {
		accumulator.usage = cloneMap(usage)
	}
	candidate := firstCandidate(inner)
	if reason := stringValue(candidate["finishReason"]); reason != "" {
		accumulator.finishReason = reason
	}
	for _, rawPart := range candidateParts(candidate) {
		part := asMap(rawPart)
		if part == nil {
			continue
		}
		if part["thought"] == true {
			accumulator.flushText()
			accumulator.thinkingText += stringValue(part["text"])
			if signature := stringValue(part["thoughtSignature"]); signature != "" {
				accumulator.thinkingSignature = signature
			}
		} else if part["functionCall"] != nil || part["inlineData"] != nil {
			accumulator.flushThinking()
			accumulator.flushText()
			accumulator.parts = append(accumulator.parts, cloneMap(part))
		} else if _, exists := part["text"]; exists {
			if stringValue(part["text"]) == "" {
				continue
			}
			accumulator.flushThinking()
			accumulator.text += stringValue(part["text"])
		}
	}
	return nil
}

func (accumulator *ThinkingAccumulator) Response(model string, cache *SignatureCache, messageID string) map[string]any {
	accumulator.flushThinking()
	accumulator.flushText()
	inner := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": accumulator.parts}, "finishReason": accumulator.finishReason,
		}},
		"usageMetadata": accumulator.usage,
	}
	return ConvertGoogleToAnthropicWithID(inner, model, cache, messageID)
}

func (accumulator *ThinkingAccumulator) flushThinking() {
	if accumulator.thinkingText == "" {
		return
	}
	accumulator.parts = append(accumulator.parts, map[string]any{
		"thought": true, "text": accumulator.thinkingText, "thoughtSignature": accumulator.thinkingSignature,
	})
	accumulator.thinkingText, accumulator.thinkingSignature = "", ""
}

func (accumulator *ThinkingAccumulator) flushText() {
	if accumulator.text == "" {
		return
	}
	accumulator.parts = append(accumulator.parts, map[string]any{"text": accumulator.text})
	accumulator.text = ""
}
