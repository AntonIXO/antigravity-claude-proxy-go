package format

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	MinSignatureLength       = 50
	GeminiMaxOutputTokens    = 16384
	GeminiSkipSignature      = "skip_thought_signature_validator"
	DefaultClaudeThinkBudget = 32000
	DefaultGeminiThinkBudget = 16000
)

type ModelFamily string

const (
	FamilyUnknown ModelFamily = "unknown"
	FamilyClaude  ModelFamily = "claude"
	FamilyGemini  ModelFamily = "gemini"
	FamilyOpenAI  ModelFamily = "openai"
)

// ModelOptions carries the live limits returned by fetchAvailableModels. The
// upstream routing ID, not name heuristics, is authoritative when these values
// are available.
type ModelOptions struct {
	SupportsThinking  bool
	ThinkingBudget    int
	MinThinkingBudget int
	MaxOutputTokens   int
}

func GetModelFamily(model string) ModelFamily {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "claude"):
		return FamilyClaude
	case strings.Contains(lower, "gemini"):
		return FamilyGemini
	case strings.Contains(lower, "gpt"):
		return FamilyOpenAI
	default:
		return FamilyUnknown
	}
}

func IsThinkingModel(model string) bool {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "claude") && strings.Contains(lower, "thinking") {
		return true
	}
	if !strings.Contains(lower, "gemini") {
		return false
	}
	if strings.Contains(lower, "thinking") {
		return true
	}
	const marker = "gemini-"
	index := strings.Index(lower, marker)
	if index < 0 {
		return false
	}
	version := lower[index+len(marker):]
	end := 0
	for end < len(version) && version[end] >= '0' && version[end] <= '9' {
		end++
	}
	major, err := strconv.Atoi(version[:end])
	return err == nil && major >= 3
}

func clampGeminiThinkingBudget(model string, value any) int {
	budget := intValue(value, DefaultGeminiThinkBudget)
	if budget == 0 {
		budget = DefaultGeminiThinkBudget
	}
	maximum := 128000
	if strings.Contains(strings.ToLower(model), "gemini-2.5") {
		maximum = 24576
	}
	if budget > maximum {
		budget = maximum
	}
	return budget
}

func asMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return nil
}

func asSlice(value any) []any {
	if value == nil {
		return nil
	}
	if result, ok := value.([]any); ok {
		return result
	}
	return nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func intValue(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case jsonNumber:
		parsed, err := strconv.Atoi(string(typed))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

// jsonNumber avoids importing encoding/json throughout the conversion files.
type jsonNumber string
