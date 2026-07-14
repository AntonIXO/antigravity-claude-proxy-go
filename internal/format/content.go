package format

import "strings"

func convertRole(role string) string {
	if role == "assistant" {
		return "model"
	}
	return "user"
}

func convertContentToParts(content any, family ModelFamily, cache *SignatureCache) []any {
	if text, ok := content.(string); ok {
		return []any{map[string]any{"text": text}}
	}
	blocks := asSlice(content)
	if blocks == nil {
		return []any{map[string]any{"text": stringValue(content)}}
	}
	parts := make([]any, 0, len(blocks))
	deferredImages := make([]any, 0)
	for _, rawBlock := range blocks {
		block := asMap(rawBlock)
		if block == nil {
			continue
		}
		switch stringValue(block["type"]) {
		case "text":
			text := stringValue(block["text"])
			if strings.TrimSpace(text) != "" {
				parts = append(parts, map[string]any{"text": text})
			}
		case "image", "document":
			source := asMap(block["source"])
			if source == nil {
				continue
			}
			if source["type"] == "base64" {
				parts = append(parts, map[string]any{"inlineData": map[string]any{
					"mimeType": source["media_type"], "data": source["data"],
				}})
			} else if source["type"] == "url" {
				mimeType := stringValue(source["media_type"])
				if mimeType == "" && block["type"] == "image" {
					mimeType = "image/jpeg"
				} else if mimeType == "" {
					mimeType = "application/pdf"
				}
				parts = append(parts, map[string]any{"fileData": map[string]any{
					"mimeType": mimeType, "fileUri": source["url"],
				}})
			}
		case "tool_use":
			call := map[string]any{"name": block["name"], "args": mapOrEmpty(block["input"])}
			if family == FamilyClaude && stringValue(block["id"]) != "" {
				call["id"] = block["id"]
			}
			part := map[string]any{"functionCall": call}
			if family == FamilyGemini {
				signature := stringValue(block["thoughtSignature"])
				if signature == "" {
					signature = cache.Tool(stringValue(block["id"]))
				}
				if signature == "" {
					signature = GeminiSkipSignature
				}
				part["thoughtSignature"] = signature
			}
			parts = append(parts, part)
		case "tool_result":
			responseContent := block["content"]
			if text, ok := responseContent.(string); ok {
				responseContent = map[string]any{"result": text}
			} else if items := asSlice(responseContent); items != nil {
				texts := make([]string, 0)
				for _, rawItem := range items {
					item := asMap(rawItem)
					if item == nil {
						continue
					}
					if item["type"] == "text" {
						texts = append(texts, stringValue(item["text"]))
					}
					if item["type"] == "image" {
						source := asMap(item["source"])
						if source != nil && source["type"] == "base64" {
							deferredImages = append(deferredImages, map[string]any{"inlineData": map[string]any{
								"mimeType": source["media_type"], "data": source["data"],
							}})
						}
					}
				}
				result := strings.Join(texts, "\n")
				if result == "" && len(deferredImages) > 0 {
					result = "Image attached"
				}
				responseContent = map[string]any{"result": result}
			}
			response := map[string]any{"name": defaultString(block["tool_use_id"], "unknown"), "response": responseContent}
			if family == FamilyClaude && stringValue(block["tool_use_id"]) != "" {
				response["id"] = block["tool_use_id"]
			}
			parts = append(parts, map[string]any{"functionResponse": response})
		case "thinking":
			signature := stringValue(block["signature"])
			if len(signature) < MinSignatureLength {
				continue
			}
			sourceFamily := cache.ThinkingFamily(signature)
			if family == FamilyClaude && sourceFamily != FamilyClaude {
				continue
			}
			if family == FamilyGemini && sourceFamily != FamilyGemini {
				continue
			}
			parts = append(parts, map[string]any{
				"text": block["thinking"], "thought": true, "thoughtSignature": signature,
			})
		}
	}
	return append(parts, deferredImages...)
}

func mapOrEmpty(value any) any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func defaultString(value any, fallback string) string {
	if result := stringValue(value); result != "" {
		return result
	}
	return fallback
}
