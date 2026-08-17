package modelcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Model is the subset of Cloud Code ModelDetails that affects agent model
// selection and request construction.
type Model struct {
	ID                       string
	UpstreamID               string
	DisplayName              string
	Description              string
	Disabled                 bool
	Recommended              bool
	SupportsThinking         bool
	SupportsAdaptiveThinking bool
	ThinkingBudget           int
	MinThinkingBudget        int
	MaxTokens                int
	MaxOutputTokens          int
	QuotaRemainingFraction   *float64
	QuotaResetTime           string
}

func (m Model) GetUpstreamID() string {
	if m.UpstreamID != "" {
		return m.UpstreamID
	}
	return m.ID
}

type Catalog struct {
	defaultID  string
	selectable []Model
	byID       map[string]Model
	byDisplay  map[string]Model
}

type SelectionError struct {
	Model string
}

func (err *SelectionError) Error() string {
	return fmt.Sprintf("model %q is not in agy's selectable agent model list", err.Model)
}

type responseDocument struct {
	Models              map[string]modelDetails `json:"models"`
	DefaultAgentModelID string                  `json:"defaultAgentModelId"`
	AgentModelSorts     []modelSort             `json:"agentModelSorts"`
}

type modelSort struct {
	Groups []modelGroup `json:"groups"`
}

type modelGroup struct {
	ModelIDs []string `json:"modelIds"`
}

type modelDetails struct {
	DisplayName              string    `json:"displayName"`
	Description              string    `json:"description"`
	Disabled                 bool      `json:"disabled"`
	Recommended              bool      `json:"recommended"`
	SupportsThinking         bool      `json:"supportsThinking"`
	SupportsAdaptiveThinking bool      `json:"supportsAdaptiveThinking"`
	ThinkingBudget           int       `json:"thinkingBudget"`
	MinThinkingBudget        int       `json:"minThinkingBudget"`
	MaxTokens                int       `json:"maxTokens"`
	MaxOutputTokens          int       `json:"maxOutputTokens"`
	QuotaInfo                quotaInfo `json:"quotaInfo"`
}

type quotaInfo struct {
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

var routingAliases = map[string]string{
	// Cloud Code publishes gemini-3.1-pro-high in models, but current agy
	// selects the separate agent route for the same display model.
	"gemini-3.1-pro-high":        "Gemini 3.1 Pro (High)",
	"gemini-3.1-pro":             "Gemini 3.1 Pro (High)",
	"gemini-pro":                 "Gemini 3.1 Pro (High)",
	"gemini-3.5-flash-high":      "Gemini 3.5 Flash (High)",
	"gemini-3.5-flash":           "Gemini 3.5 Flash (High)",
	"gemini-3.5-flash-medium":    "Gemini 3.5 Flash (Medium)",
	"gemini-3.6-flash":           "Gemini 3.6 Flash (High)",
	"gemini-3.7-flash":           "Gemini 3.7 Flash (High)",
	"claude-sonnet-4-6-thinking": "Claude Sonnet 4.6 (Thinking)",
	"gemini-3.7-flash-high":      "Gemini 3.7 Flash (High)",
	"gemini-3.7-flash-medium":    "Gemini 3.7 Flash (Medium)",
	"gemini-3.7-flash-low":       "Gemini 3.7 Flash (Low)",
}

func Parse(body []byte) (*Catalog, error) {
	var document responseDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("decode Cloud Code model catalog: %w", err)
	}
	if len(document.Models) == 0 {
		return nil, errors.New("Cloud Code model catalog is empty")
	}

	ids := make([]string, 0)
	seen := make(map[string]bool)
	for _, modelSort := range document.AgentModelSorts {
		for _, group := range modelSort.Groups {
			for _, id := range group.ModelIDs {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	// Older responses did not include agentModelSorts. Keep a deterministic
	// compatibility fallback, while current agy's explicit list remains the
	// authoritative path.
	if len(ids) == 0 {
		for id, details := range document.Models {
			if details.DisplayName != "" && !details.Disabled && isAgentFamily(id) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
	}

	catalog := &Catalog{
		defaultID: document.DefaultAgentModelID,
		byID:      make(map[string]Model),
		byDisplay: make(map[string]Model),
	}
	for _, id := range ids {
		details, exists := document.Models[id]
		if !exists || details.Disabled {
			continue
		}
		model := Model{
			ID: id, DisplayName: details.DisplayName, Description: details.Description,
			Disabled: details.Disabled, Recommended: details.Recommended,
			SupportsThinking:         details.SupportsThinking,
			SupportsAdaptiveThinking: details.SupportsAdaptiveThinking,
			ThinkingBudget:           details.ThinkingBudget, MinThinkingBudget: details.MinThinkingBudget,
			MaxTokens: details.MaxTokens, MaxOutputTokens: details.MaxOutputTokens,
			QuotaRemainingFraction: details.QuotaInfo.RemainingFraction,
			QuotaResetTime:         details.QuotaInfo.ResetTime,
		}
		if model.DisplayName == "" {
			model.DisplayName = id
		}
		catalog.selectable = append(catalog.selectable, model)
		catalog.byID[strings.ToLower(id)] = model
		catalog.byDisplay[strings.ToLower(model.DisplayName)] = model
	}

	synthetic37 := []struct {
		id          string
		displayName string
		baseID      string
	}{
		{"gemini-3.7-flash-high", "Gemini 3.7 Flash (High)", "gemini-3.6-flash-high"},
		{"gemini-3.7-flash-medium", "Gemini 3.7 Flash (Medium)", "gemini-3.6-flash-medium"},
		{"gemini-3.7-flash-low", "Gemini 3.7 Flash (Low)", "gemini-3.6-flash-low"},
	}

	var syntheticModels []Model
	for _, synth := range synthetic37 {
		if _, exists := catalog.byID[synth.id]; !exists {
			if baseModel, exists := catalog.byID[synth.baseID]; exists {
				synthModel := baseModel
				synthModel.ID = synth.id
				synthModel.UpstreamID = synth.baseID
				synthModel.DisplayName = synth.displayName
				catalog.byID[synth.id] = synthModel
				catalog.byDisplay[strings.ToLower(synth.displayName)] = synthModel
				syntheticModels = append(syntheticModels, synthModel)
			}
		}
	}
	if len(syntheticModels) > 0 {
		catalog.selectable = append(syntheticModels, catalog.selectable...)
	}

	if len(catalog.selectable) == 0 {
		return nil, errors.New("Cloud Code catalog has no selectable agent models")
	}
	return catalog, nil
}

func (catalog *Catalog) DefaultID() string {
	if catalog == nil {
		return ""
	}
	if model, exists := catalog.byID[strings.ToLower(catalog.defaultID)]; exists {
		return model.ID
	}
	return catalog.selectable[0].ID
}

func (catalog *Catalog) Selectable() []Model {
	if catalog == nil {
		return nil
	}
	return append([]Model(nil), catalog.selectable...)
}

func (catalog *Catalog) Resolve(requested string) (Model, error) {
	if catalog == nil {
		return Model{}, errors.New("model catalog is unavailable")
	}
	key := strings.ToLower(strings.TrimSpace(requested))
	if key == "" {
		key = strings.ToLower(catalog.DefaultID())
	}
	if model, exists := catalog.byID[key]; exists {
		return model, nil
	}
	if model, exists := catalog.byDisplay[key]; exists {
		return model, nil
	}
	if displayName := routingAliases[key]; displayName != "" {
		if model, exists := catalog.byDisplay[strings.ToLower(displayName)]; exists {
			return model, nil
		}
	}
	return Model{}, &SelectionError{Model: requested}
}

func ExtractReasoningParams(request map[string]any) (effort string, budget int, hasBudget bool, disabled bool) {
	if request == nil {
		return "", 0, false, false
	}
	if val, ok := request["reasoning_effort"]; ok {
		effort = strings.ToLower(fmt.Sprint(val))
	}
	if effort == "" {
		if val, ok := request["reasoning"]; ok {
			effort = strings.ToLower(fmt.Sprint(val))
		}
	}
	if thinking, ok := request["thinking"].(map[string]any); ok {
		if tType, exists := thinking["type"]; exists && strings.ToLower(fmt.Sprint(tType)) == "disabled" {
			disabled = true
		}
		if b, exists := thinking["budget_tokens"]; exists {
			switch v := b.(type) {
			case float64:
				budget = int(v)
				hasBudget = true
			case int:
				budget = v
				hasBudget = true
			case int64:
				budget = int(v)
				hasBudget = true
			}
		}
	}
	if b, ok := request["thinking_budget"]; ok {
		switch v := b.(type) {
		case float64:
			budget = int(v)
			hasBudget = true
		case int:
			budget = v
			hasBudget = true
		case int64:
			budget = int(v)
			hasBudget = true
		}
	}
	if effort == "none" || effort == "disabled" {
		disabled = true
	}
	if hasBudget && budget <= 0 && !disabled {
		thinking, _ := request["thinking"].(map[string]any)
		if thinking == nil || strings.ToLower(fmt.Sprint(thinking["type"])) != "enabled" {
			disabled = true
		}
	}
	if !disabled && effort == "" && hasBudget && budget > 0 {
		switch {
		case budget <= 2048:
			effort = "low"
		case budget < 12000:
			effort = "medium"
		default:
			effort = "high"
		}
	}
	return effort, budget, hasBudget, disabled
}

func (catalog *Catalog) ResolveWithRequest(requested string, request map[string]any) (Model, error) {
	model, err := catalog.Resolve(requested)
	if err != nil {
		return Model{}, err
	}
	if request == nil {
		return model, nil
	}

	effort, budget, hasBudget, disabled := ExtractReasoningParams(request)

	if disabled {
		model.SupportsThinking = false
		model.ThinkingBudget = 0
		return model, nil
	}

	if effort != "" {
		targetID := ""
		lowerReq := strings.ToLower(strings.TrimSpace(requested))
		switch {
		case strings.HasPrefix(lowerReq, "gemini-3.7-flash"):
			targetID = "gemini-3.7-flash-" + effort
		case strings.HasPrefix(lowerReq, "gemini-3.6-flash"):
			targetID = "gemini-3.6-flash-" + effort
		case strings.HasPrefix(lowerReq, "gemini-3.5-flash"):
			targetID = "gemini-3.5-flash-" + effort
		}
		if targetID != "" {
			if variant, err := catalog.Resolve(targetID); err == nil {
				if hasBudget && budget > 0 {
					variant.ThinkingBudget = budget
				}
				return variant, nil
			}
		}
	}

	if hasBudget && budget > 0 {
		model.ThinkingBudget = budget
	}
	return model, nil
}

func isAgentFamily(id string) bool {
	lower := strings.ToLower(id)
	return strings.Contains(lower, "claude") || strings.Contains(lower, "gemini") || strings.Contains(lower, "gpt")
}
