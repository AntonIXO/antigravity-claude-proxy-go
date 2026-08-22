package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var DefaultClaudePresets = []map[string]any{
	{
		"name": "Claude Thinking",
		"config": map[string]any{
			"ANTHROPIC_AUTH_TOKEN": "test",
			"ANTHROPIC_BASE_URL":   "http://localhost:8080",
			"ANTHROPIC_MODEL":      "claude-opus-4-6-thinking",
		},
	},
	{
		"name": "Claude Sonnet",
		"config": map[string]any{
			"ANTHROPIC_AUTH_TOKEN": "test",
			"ANTHROPIC_BASE_URL":   "http://localhost:8080",
			"ANTHROPIC_MODEL":      "claude-sonnet-4-6",
		},
	},
	{
		"name": "Gemini Flash",
		"config": map[string]any{
			"ANTHROPIC_AUTH_TOKEN": "test",
			"ANTHROPIC_BASE_URL":   "http://localhost:8080",
			"ANTHROPIC_MODEL":      "gemini-3.5-flash-low",
		},
	},
}

var DefaultServerPresets = []map[string]any{
	{
		"name":        "Conservative",
		"description": "Lower concurrency, higher retry delays, safe for single account",
		"builtIn":     true,
		"config": map[string]any{
			"maxRetries":        3,
			"retryBaseMs":       2000,
			"retryMaxMs":        60000,
			"defaultCooldownMs": 30000,
		},
	},
	{
		"name":        "Balanced",
		"description": "Recommended settings for multi-account pools with smart distribution",
		"builtIn":     true,
		"config": map[string]any{
			"maxRetries":        5,
			"retryBaseMs":       1000,
			"retryMaxMs":        30000,
			"defaultCooldownMs": 10000,
		},
	},
	{
		"name":        "High Throughput",
		"description": "Aggressive rotation for large multi-account pools (5+ accounts)",
		"builtIn":     true,
		"config": map[string]any{
			"maxRetries":        8,
			"retryBaseMs":       500,
			"retryMaxMs":        15000,
			"defaultCooldownMs": 5000,
		},
	},
}

// ClaudeConfigPath returns path to Claude CLI settings.json.
func ClaudeConfigPath() (string, error) {
	if custom := os.Getenv("CLAUDE_CONFIG_PATH"); custom != "" {
		if strings.HasSuffix(custom, "settings.json") {
			return custom, nil
		}
		return filepath.Join(custom, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// ReadClaudeConfig reads the Claude CLI configuration.
func ReadClaudeConfig() (map[string]any, error) {
	path, err := ClaudeConfigPath()
	if err != nil {
		return map[string]any{"env": map[string]any{}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{"env": map[string]any{}}, nil
		}
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return map[string]any{"env": map[string]any{}}, nil
	}
	return config, nil
}

// ReplaceClaudeConfig completely writes new config to settings.json.
func ReplaceClaudeConfig(newConfig map[string]any) error {
	path, err := ClaudeConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(newConfig, "", "  ")
	if err != nil {
		return err
	}
	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpFile, path)
}

// UpdateClaudeConfig deep merges updates into existing Claude settings.json.
func UpdateClaudeConfig(updates map[string]any) (map[string]any, error) {
	current, err := ReadClaudeConfig()
	if err != nil {
		current = make(map[string]any)
	}
	for k, v := range updates {
		if vMap, ok := v.(map[string]any); ok {
			if existingMap, ok := current[k].(map[string]any); ok {
				for vk, vv := range vMap {
					existingMap[vk] = vv
				}
				current[k] = existingMap
				continue
			}
		}
		current[k] = v
	}
	if err := ReplaceClaudeConfig(current); err != nil {
		return nil, err
	}
	return current, nil
}

// RestoreClaudeConfig removes all proxy environment variables.
func RestoreClaudeConfig() (map[string]any, error) {
	current, err := ReadClaudeConfig()
	if err != nil {
		return nil, err
	}
	proxyVars := []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ENABLE_EXPERIMENTAL_MCP_CLI",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
		"CLAUDE_CODE_SKIP_FAST_MODE_ORG_CHECK",
		"ANTHROPIC_API_KEY",
	}
	if env, ok := current["env"].(map[string]any); ok {
		for _, key := range proxyVars {
			delete(env, key)
		}
		if len(env) == 0 {
			delete(current, "env")
		}
	}
	if err := ReplaceClaudeConfig(current); err != nil {
		return nil, err
	}
	return current, nil
}

// ClaudePresetsFilePath returns path to ~/.config/antigravity-proxy/claude-presets.json.
func ClaudePresetsFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "antigravity-proxy", "claude-presets.json"), nil
}

// ReadClaudePresets returns all Claude CLI presets (defaults + custom).
func ReadClaudePresets() ([]map[string]any, error) {
	path, err := ClaudePresetsFilePath()
	if err != nil {
		return DefaultClaudePresets, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultClaudePresets, nil
		}
		return nil, err
	}
	var presets []map[string]any
	if err := json.Unmarshal(data, &presets); err != nil {
		return DefaultClaudePresets, nil
	}
	return presets, nil
}

// SaveClaudePreset adds or updates a Claude CLI preset.
func SaveClaudePreset(name string, presetConfig map[string]any) ([]map[string]any, error) {
	presets, err := ReadClaudePresets()
	if err != nil {
		presets = DefaultClaudePresets
	}
	found := false
	for i, p := range presets {
		if pName, _ := p["name"].(string); strings.EqualFold(pName, name) {
			presets[i]["config"] = presetConfig
			found = true
			break
		}
	}
	if !found {
		presets = append(presets, map[string]any{
			"name":   name,
			"config": presetConfig,
		})
	}

	path, err := ClaudePresetsFilePath()
	if err != nil {
		return presets, err
	}
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	data, _ := json.MarshalIndent(presets, "", "  ")
	_ = os.WriteFile(path, data, 0600)

	return presets, nil
}

// DeleteClaudePreset deletes a Claude CLI preset.
func DeleteClaudePreset(name string) ([]map[string]any, error) {
	presets, err := ReadClaudePresets()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(presets))
	for _, p := range presets {
		if pName, _ := p["name"].(string); !strings.EqualFold(pName, name) {
			result = append(result, p)
		}
	}
	path, err := ClaudePresetsFilePath()
	if err != nil {
		return result, err
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	_ = os.WriteFile(path, data, 0600)
	return result, nil
}

// ServerPresetsFilePath returns path to ~/.config/antigravity-proxy/server-presets.json.
func ServerPresetsFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "antigravity-proxy", "server-presets.json"), nil
}

// ReadServerPresets returns all server configuration presets.
func ReadServerPresets() ([]map[string]any, error) {
	path, err := ServerPresetsFilePath()
	if err != nil {
		return DefaultServerPresets, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultServerPresets, nil
		}
		return nil, err
	}
	var customPresets []map[string]any
	if err := json.Unmarshal(data, &customPresets); err != nil {
		return DefaultServerPresets, nil
	}

	// Combine built-in with custom
	result := make([]map[string]any, 0, len(DefaultServerPresets)+len(customPresets))
	result = append(result, DefaultServerPresets...)
	result = append(result, customPresets...)
	return result, nil
}

// SaveServerPreset adds a custom server preset.
func SaveServerPreset(name string, presetConfig map[string]any, description string) ([]map[string]any, error) {
	for _, builtIn := range DefaultServerPresets {
		if strings.EqualFold(builtIn["name"].(string), name) {
			return nil, errors.New("cannot overwrite built-in preset")
		}
	}

	path, err := ServerPresetsFilePath()
	if err != nil {
		return nil, err
	}
	var customPresets []map[string]any
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &customPresets)
	}

	found := false
	for i, p := range customPresets {
		if pName, _ := p["name"].(string); strings.EqualFold(pName, name) {
			customPresets[i]["config"] = presetConfig
			customPresets[i]["description"] = description
			found = true
			break
		}
	}
	if !found {
		customPresets = append(customPresets, map[string]any{
			"name":        name,
			"description": description,
			"config":      presetConfig,
		})
	}

	_ = os.MkdirAll(filepath.Dir(path), 0755)
	data, _ := json.MarshalIndent(customPresets, "", "  ")
	_ = os.WriteFile(path, data, 0600)

	return ReadServerPresets()
}

// DeleteServerPreset deletes a custom server preset.
func DeleteServerPreset(name string) ([]map[string]any, error) {
	for _, builtIn := range DefaultServerPresets {
		if strings.EqualFold(builtIn["name"].(string), name) {
			return nil, errors.New("cannot delete built-in preset")
		}
	}
	path, err := ServerPresetsFilePath()
	if err != nil {
		return nil, err
	}
	var customPresets []map[string]any
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &customPresets)
	}
	result := make([]map[string]any, 0, len(customPresets))
	for _, p := range customPresets {
		if pName, _ := p["name"].(string); !strings.EqualFold(pName, name) {
			result = append(result, p)
		}
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	_ = os.WriteFile(path, data, 0600)

	return ReadServerPresets()
}
