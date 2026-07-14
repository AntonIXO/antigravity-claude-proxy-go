package format

import "strings"

func cleanCacheControl(messages []any) []any {
	cleaned := make([]any, 0, len(messages))
	for _, rawMessage := range messages {
		message := asMap(rawMessage)
		if message == nil {
			cleaned = append(cleaned, rawMessage)
			continue
		}
		copyMessage := cloneMap(message)
		if blocks := asSlice(copyMessage["content"]); blocks != nil {
			copyBlocks := make([]any, 0, len(blocks))
			for _, rawBlock := range blocks {
				block := asMap(rawBlock)
				if block == nil {
					copyBlocks = append(copyBlocks, rawBlock)
					continue
				}
				copyBlock := cloneMap(block)
				delete(copyBlock, "cache_control")
				copyBlocks = append(copyBlocks, copyBlock)
			}
			copyMessage["content"] = copyBlocks
		}
		cleaned = append(cleaned, copyMessage)
	}
	return cleaned
}

func isThinkingPart(block map[string]any) bool {
	return block["type"] == "thinking" || block["type"] == "redacted_thinking" || block["thinking"] != nil || block["thought"] == true
}

func hasValidSignature(block map[string]any) bool {
	if block["thought"] == true {
		return len(stringValue(block["thoughtSignature"])) >= MinSignatureLength
	}
	return len(stringValue(block["signature"])) >= MinSignatureLength
}

func hasGeminiHistory(messages []any) bool {
	for _, rawMessage := range messages {
		for _, rawBlock := range asSlice(asMap(rawMessage)["content"]) {
			block := asMap(rawBlock)
			if block != nil && block["type"] == "tool_use" {
				if _, exists := block["thoughtSignature"]; exists {
					return true
				}
			}
		}
	}
	return false
}

func hasUnsignedThinkingBlocks(messages []any) bool {
	for _, rawMessage := range messages {
		message := asMap(rawMessage)
		if message == nil || (message["role"] != "assistant" && message["role"] != "model") {
			continue
		}
		for _, rawBlock := range asSlice(message["content"]) {
			block := asMap(rawBlock)
			if block != nil && isThinkingPart(block) && !hasValidSignature(block) {
				return true
			}
		}
	}
	return false
}

func restoreThinkingSignatures(content []any) []any {
	result := make([]any, 0, len(content))
	for _, rawBlock := range content {
		block := asMap(rawBlock)
		if block == nil || block["type"] != "thinking" {
			result = append(result, rawBlock)
			continue
		}
		if hasValidSignature(block) {
			result = append(result, sanitizeThinkingBlock(block))
		}
	}
	return result
}

func removeTrailingThinkingBlocks(content []any) []any {
	end := len(content)
	for index := len(content) - 1; index >= 0; index-- {
		block := asMap(content[index])
		if block == nil || !isThinkingPart(block) {
			break
		}
		if hasValidSignature(block) {
			break
		}
		end = index
	}
	return content[:end]
}

func reorderAssistantContent(content []any) []any {
	if len(content) == 1 {
		block := asMap(content[0])
		if block != nil && (block["type"] == "thinking" || block["type"] == "redacted_thinking") {
			return []any{sanitizeThinkingBlock(block)}
		}
		return content
	}
	thinking := make([]any, 0)
	text := make([]any, 0)
	tools := make([]any, 0)
	for _, rawBlock := range content {
		block := asMap(rawBlock)
		if block == nil {
			continue
		}
		switch block["type"] {
		case "thinking", "redacted_thinking":
			thinking = append(thinking, sanitizeThinkingBlock(block))
		case "tool_use":
			tools = append(tools, copyFields(block, "type", "id", "name", "input", "thoughtSignature"))
		case "text":
			if strings.TrimSpace(stringValue(block["text"])) != "" {
				text = append(text, copyFields(block, "type", "text"))
			}
		default:
			text = append(text, block)
		}
	}
	result := append(thinking, text...)
	return append(result, tools...)
}

func sanitizeThinkingBlock(block map[string]any) map[string]any {
	if block["thought"] == true {
		return copyFields(block, "thought", "text", "thoughtSignature")
	}
	if block["type"] == "redacted_thinking" {
		return copyFields(block, "type", "data")
	}
	return copyFields(block, "type", "thinking", "signature")
}

func filterUnsignedThinkingParts(parts []any) []any {
	result := make([]any, 0, len(parts))
	for _, rawPart := range parts {
		part := asMap(rawPart)
		if part == nil || !isThinkingPart(part) {
			result = append(result, rawPart)
		} else if hasValidSignature(part) {
			result = append(result, sanitizeThinkingBlock(part))
		}
	}
	return result
}

type conversationState struct {
	inToolLoop      bool
	interruptedTool bool
	hasThinking     bool
	toolResultCount int
	lastAssistant   int
}

func analyzeConversation(messages []any) conversationState {
	state := conversationState{lastAssistant: -1}
	for index := len(messages) - 1; index >= 0; index-- {
		role := asMap(messages[index])["role"]
		if role == "assistant" || role == "model" {
			state.lastAssistant = index
			break
		}
	}
	if state.lastAssistant < 0 {
		return state
	}
	assistant := asMap(messages[state.lastAssistant])
	hasToolUse := messageHasBlock(assistant, "tool_use", "functionCall")
	state.hasThinking = messageHasValidThinking(assistant)
	plainUser := false
	for _, rawMessage := range messages[state.lastAssistant+1:] {
		message := asMap(rawMessage)
		if messageHasBlock(message, "tool_result", "functionResponse") {
			state.toolResultCount++
		}
		if isPlainUserMessage(message) {
			plainUser = true
		}
	}
	state.inToolLoop = hasToolUse && state.toolResultCount > 0
	state.interruptedTool = hasToolUse && state.toolResultCount == 0 && plainUser
	return state
}

func needsThinkingRecovery(messages []any) bool {
	state := analyzeConversation(messages)
	return (state.inToolLoop || state.interruptedTool) && !state.hasThinking
}

func closeToolLoopForThinking(messages []any, family ModelFamily, cache *SignatureCache) []any {
	state := analyzeConversation(messages)
	if !state.inToolLoop && !state.interruptedTool {
		return messages
	}
	modified := stripInvalidThinkingBlocks(messages, family, cache)
	if state.interruptedTool {
		synthetic := map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "[Tool call was interrupted.]"}}}
		index := state.lastAssistant + 1
		modified = append(modified, nil)
		copy(modified[index+1:], modified[index:])
		modified[index] = synthetic
		return modified
	}
	message := "[Tool execution completed.]"
	if state.toolResultCount != 1 {
		message = "[" + stringValue(state.toolResultCount) + " tool executions completed.]"
	}
	return append(modified,
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": message}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "[Continue]"}}},
	)
}

func stripInvalidThinkingBlocks(messages []any, family ModelFamily, cache *SignatureCache) []any {
	result := make([]any, 0, len(messages))
	for _, rawMessage := range messages {
		message := asMap(rawMessage)
		if message == nil {
			result = append(result, rawMessage)
			continue
		}
		copyMessage := cloneMap(message)
		key := "content"
		parts := asSlice(copyMessage[key])
		if parts == nil {
			key, parts = "parts", asSlice(copyMessage["parts"])
		}
		if parts == nil {
			result = append(result, copyMessage)
			continue
		}
		filtered := make([]any, 0, len(parts))
		for _, rawPart := range parts {
			part := asMap(rawPart)
			if part == nil || !isThinkingPart(part) {
				filtered = append(filtered, rawPart)
				continue
			}
			if !hasValidSignature(part) {
				continue
			}
			if family == FamilyGemini {
				signature := stringValue(part["signature"])
				if part["thought"] == true {
					signature = stringValue(part["thoughtSignature"])
				}
				if cache.ThinkingFamily(signature) != FamilyGemini {
					continue
				}
			}
			filtered = append(filtered, rawPart)
		}
		if len(filtered) == 0 {
			if key == "content" {
				filtered = []any{map[string]any{"type": "text", "text": "."}}
			} else {
				filtered = []any{map[string]any{"text": "."}}
			}
		}
		copyMessage[key] = filtered
		result = append(result, copyMessage)
	}
	return result
}

func messageHasValidThinking(message map[string]any) bool {
	parts := asSlice(message["content"])
	if parts == nil {
		parts = asSlice(message["parts"])
	}
	for _, rawPart := range parts {
		part := asMap(rawPart)
		if part != nil && isThinkingPart(part) && hasValidSignature(part) {
			return true
		}
	}
	return false
}

func messageHasBlock(message map[string]any, anthropicType, googleField string) bool {
	parts := asSlice(message["content"])
	if parts == nil {
		parts = asSlice(message["parts"])
	}
	for _, rawPart := range parts {
		part := asMap(rawPart)
		if part != nil && (part["type"] == anthropicType || part[googleField] != nil) {
			return true
		}
	}
	return false
}

func isPlainUserMessage(message map[string]any) bool {
	if message == nil || message["role"] != "user" {
		return false
	}
	if _, ok := message["content"].(string); ok {
		return true
	}
	return !messageHasBlock(message, "tool_result", "functionResponse")
}

func copyFields(source map[string]any, fields ...string) map[string]any {
	result := make(map[string]any)
	for _, field := range fields {
		if value, exists := source[field]; exists {
			result[field] = cloneJSON(value)
		}
	}
	return result
}
