package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.LogLevel != "info" {
		t.Errorf("expected LogLevel info, got %s", cfg.LogLevel)
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("expected MaxRetries 5, got %d", cfg.MaxRetries)
	}
	if cfg.AccountSelection.Strategy != "hybrid" {
		t.Errorf("expected Strategy hybrid, got %s", cfg.AccountSelection.Strategy)
	}
}

func TestClaudePresets(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	presets, err := ReadClaudePresets()
	if err != nil {
		t.Fatalf("ReadClaudePresets error: %v", err)
	}
	if len(presets) < 3 {
		t.Errorf("expected at least 3 default presets, got %d", len(presets))
	}

	// Save new custom preset
	customConfig := map[string]any{"ANTHROPIC_MODEL": "custom-model"}
	updated, err := SaveClaudePreset("My Preset", customConfig)
	if err != nil {
		t.Fatalf("SaveClaudePreset error: %v", err)
	}
	if len(updated) <= len(presets) {
		t.Errorf("expected preset count to increase")
	}

	// Delete custom preset
	deleted, err := DeleteClaudePreset("My Preset")
	if err != nil {
		t.Fatalf("DeleteClaudePreset error: %v", err)
	}
	if len(deleted) != len(presets) {
		t.Errorf("expected preset count to return to %d, got %d", len(presets), len(deleted))
	}
}

func TestServerPresets(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	presets, err := ReadServerPresets()
	if err != nil {
		t.Fatalf("ReadServerPresets error: %v", err)
	}
	if len(presets) < 3 {
		t.Errorf("expected at least 3 default server presets, got %d", len(presets))
	}

	// Cannot delete built-in preset
	_, err = DeleteServerPreset("Balanced")
	if err == nil {
		t.Errorf("expected error deleting built-in preset")
	}
}

func TestClaudeConfigOperations(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	_ = os.MkdirAll(claudeDir, 0755)
	settingsPath := filepath.Join(claudeDir, "settings.json")
	t.Setenv("CLAUDE_CONFIG_PATH", settingsPath)

	// Update config
	_, err := UpdateClaudeConfig(map[string]any{
		"env": map[string]any{
			"ANTHROPIC_BASE_URL": "http://localhost:8080",
			"CUSTOM_VAR":         "value123",
		},
	})
	if err != nil {
		t.Fatalf("UpdateClaudeConfig error: %v", err)
	}

	read, err := ReadClaudeConfig()
	if err != nil {
		t.Fatalf("ReadClaudeConfig error: %v", err)
	}
	env, ok := read["env"].(map[string]any)
	if !ok || env["ANTHROPIC_BASE_URL"] != "http://localhost:8080" {
		t.Errorf("expected ANTHROPIC_BASE_URL http://localhost:8080")
	}

	// Restore config
	restored, err := RestoreClaudeConfig()
	if err != nil {
		t.Fatalf("RestoreClaudeConfig error: %v", err)
	}
	restoredEnv, _ := restored["env"].(map[string]any)
	if _, exists := restoredEnv["ANTHROPIC_BASE_URL"]; exists {
		t.Errorf("expected ANTHROPIC_BASE_URL to be removed")
	}
	if restoredEnv["CUSTOM_VAR"] != "value123" {
		t.Errorf("expected CUSTOM_VAR to be preserved")
	}
}
