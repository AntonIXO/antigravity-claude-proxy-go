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
	DisplayName              string `json:"displayName"`
	Description              string `json:"description"`
	Disabled                 bool   `json:"disabled"`
	Recommended              bool   `json:"recommended"`
	SupportsThinking         bool   `json:"supportsThinking"`
	SupportsAdaptiveThinking bool   `json:"supportsAdaptiveThinking"`
	ThinkingBudget           int    `json:"thinkingBudget"`
	MinThinkingBudget        int    `json:"minThinkingBudget"`
	MaxTokens                int    `json:"maxTokens"`
	MaxOutputTokens          int    `json:"maxOutputTokens"`
}

var routingAliases = map[string]string{
	// Cloud Code publishes gemini-3.1-pro-high in models, but current agy
	// selects the separate agent route for the same display model.
	"gemini-3.1-pro-high":        "Gemini 3.1 Pro (High)",
	"gemini-3.5-flash-high":      "Gemini 3.5 Flash (High)",
	"gemini-3.5-flash-medium":    "Gemini 3.5 Flash (Medium)",
	"claude-sonnet-4-6-thinking": "Claude Sonnet 4.6 (Thinking)",
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
		}
		if model.DisplayName == "" {
			model.DisplayName = id
		}
		catalog.selectable = append(catalog.selectable, model)
		catalog.byID[strings.ToLower(id)] = model
		catalog.byDisplay[strings.ToLower(model.DisplayName)] = model
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

func isAgentFamily(id string) bool {
	lower := strings.ToLower(id)
	return strings.Contains(lower, "claude") || strings.Contains(lower, "gemini") || strings.Contains(lower, "gpt")
}
