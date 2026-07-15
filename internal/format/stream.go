package format

import (
	"encoding/json"
	"errors"
)

var ErrEmptyResponse = errors.New("no content parts received from Cloud Code")

type StreamConverter struct {
	model                    string
	messageID                string
	cache                    *SignatureCache
	hasStarted               bool
	blockIndex               int
	currentBlock             string
	currentThinkingSignature string
	inputTokens              int
	outputTokens             int
	cacheReadTokens          int
	stopReason               string
}

func NewStreamConverter(model string, cache *SignatureCache, messageID string) *StreamConverter {
	if messageID == "" {
		messageID = "msg_" + randomHex(16)
	}
	return &StreamConverter{model: model, cache: cache, messageID: messageID}
}

func (converter *StreamConverter) Consume(data []byte) ([]map[string]any, error) {
	inner, err := decodeCloudCodeEvent(data)
	if err != nil || inner == nil {
		// Malformed data frames are ignored.
		return nil, nil
	}
	if usage := asMap(inner["usageMetadata"]); usage != nil {
		if value := intValue(usage["promptTokenCount"], 0); value != 0 {
			converter.inputTokens = value
		}
		if value := intValue(usage["candidatesTokenCount"], 0); value != 0 {
			converter.outputTokens = value
		}
		if value := intValue(usage["cachedContentTokenCount"], 0); value != 0 {
			converter.cacheReadTokens = value
		}
	}
	candidate := firstCandidate(inner)
	parts := candidateParts(candidate)
	events := make([]map[string]any, 0, 2)
	if !converter.hasStarted && len(parts) > 0 {
		converter.hasStarted = true
		events = append(events, map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": converter.messageID, "type": "message", "role": "assistant",
				"content": []any{}, "model": converter.model, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":                converter.inputTokens - converter.cacheReadTokens,
					"output_tokens":               0,
					"cache_read_input_tokens":     converter.cacheReadTokens,
					"cache_creation_input_tokens": 0,
				},
			},
		})
	}
	for _, rawPart := range parts {
		part := asMap(rawPart)
		if part == nil {
			continue
		}
		if part["thought"] == true {
			if converter.currentBlock != "thinking" {
				events = append(events, converter.closeCurrent()...)
				converter.currentBlock = "thinking"
				converter.currentThinkingSignature = ""
				events = append(events, map[string]any{
					"type": "content_block_start", "index": converter.blockIndex,
					"content_block": map[string]any{"type": "thinking", "thinking": ""},
				})
			}
			signature := stringValue(part["thoughtSignature"])
			if len(signature) >= MinSignatureLength {
				converter.currentThinkingSignature = signature
				converter.cache.CacheThinking(signature, GetModelFamily(converter.model))
			}
			events = append(events, map[string]any{
				"type": "content_block_delta", "index": converter.blockIndex,
				"delta": map[string]any{"type": "thinking_delta", "thinking": stringValue(part["text"])},
			})
			continue
		}
		if text, exists := part["text"]; exists {
			if stringValue(text) == "" {
				continue
			}
			if converter.currentBlock != "text" {
				events = append(events, converter.closeCurrent()...)
				converter.currentBlock = "text"
				events = append(events, map[string]any{
					"type": "content_block_start", "index": converter.blockIndex,
					"content_block": map[string]any{"type": "text", "text": ""},
				})
			}
			events = append(events, map[string]any{
				"type": "content_block_delta", "index": converter.blockIndex,
				"delta": map[string]any{"type": "text_delta", "text": text},
			})
			continue
		}
		if call := asMap(part["functionCall"]); call != nil {
			events = append(events, converter.closeCurrent()...)
			converter.currentBlock = "tool_use"
			converter.stopReason = "tool_use"
			toolID := stringValue(call["id"])
			if toolID == "" {
				toolID = "toolu_" + randomHex(12)
			}
			block := map[string]any{"type": "tool_use", "id": toolID, "name": call["name"], "input": map[string]any{}}
			signature := stringValue(part["thoughtSignature"])
			if len(signature) >= MinSignatureLength {
				block["thoughtSignature"] = signature
				converter.cache.CacheTool(toolID, signature)
			}
			encoded, _ := json.Marshal(mapOrEmpty(call["args"]))
			events = append(events,
				map[string]any{"type": "content_block_start", "index": converter.blockIndex, "content_block": block},
				map[string]any{
					"type": "content_block_delta", "index": converter.blockIndex,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": string(encoded)},
				},
			)
			continue
		}
		if inline := asMap(part["inlineData"]); inline != nil {
			events = append(events, converter.closeCurrent()...)
			converter.currentBlock = "image"
			events = append(events,
				map[string]any{
					"type": "content_block_start", "index": converter.blockIndex,
					"content_block": map[string]any{
						"type": "image", "source": map[string]any{
							"type": "base64", "media_type": inline["mimeType"], "data": inline["data"],
						},
					},
				},
				map[string]any{"type": "content_block_stop", "index": converter.blockIndex},
			)
			converter.blockIndex++
			converter.currentBlock = ""
		}
	}
	if converter.stopReason == "" {
		switch stringValue(candidate["finishReason"]) {
		case "MAX_TOKENS":
			converter.stopReason = "max_tokens"
		case "STOP":
			converter.stopReason = "end_turn"
		}
	}
	return events, nil
}

func (converter *StreamConverter) Finish() ([]map[string]any, error) {
	if !converter.hasStarted {
		return nil, ErrEmptyResponse
	}
	events := converter.closeCurrent()
	stopReason := converter.stopReason
	if stopReason == "" {
		stopReason = "end_turn"
	}
	return append(events,
		map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": map[string]any{
				"output_tokens":               converter.outputTokens,
				"cache_read_input_tokens":     converter.cacheReadTokens,
				"cache_creation_input_tokens": 0,
			},
		},
		map[string]any{"type": "message_stop"},
	), nil
}

func (converter *StreamConverter) closeCurrent() []map[string]any {
	if converter.currentBlock == "" {
		return nil
	}
	events := make([]map[string]any, 0, 2)
	if converter.currentBlock == "thinking" && converter.currentThinkingSignature != "" {
		events = append(events, map[string]any{
			"type": "content_block_delta", "index": converter.blockIndex,
			"delta": map[string]any{"type": "signature_delta", "signature": converter.currentThinkingSignature},
		})
		converter.currentThinkingSignature = ""
	}
	events = append(events, map[string]any{"type": "content_block_stop", "index": converter.blockIndex})
	converter.blockIndex++
	converter.currentBlock = ""
	return events
}

func decodeCloudCodeEvent(data []byte) (map[string]any, error) {
	if string(data) == "[DONE]" || len(data) == 0 {
		return nil, nil
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if inner := asMap(value["response"]); inner != nil {
		return inner, nil
	}
	return value, nil
}

func firstCandidate(response map[string]any) map[string]any {
	candidates := asSlice(response["candidates"])
	if len(candidates) == 0 {
		return nil
	}
	return asMap(candidates[0])
}

func candidateParts(candidate map[string]any) []any {
	return asSlice(asMap(candidate["content"])["parts"])
}
