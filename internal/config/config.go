package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type EndpointConfig struct {
	URL    string `json:"url"`
	APIKey string `json:"apiKey,omitempty"`
}

type AccountSelectionConfig struct {
	Strategy    string         `json:"strategy,omitempty"`
	HealthScore map[string]any `json:"healthScore,omitempty"`
	TokenBucket map[string]any `json:"tokenBucket,omitempty"`
	Quota       map[string]any `json:"quota,omitempty"`
	Weights     map[string]any `json:"weights,omitempty"`
}

type Config struct {
	APIKey                   string                 `json:"apiKey,omitempty"`
	WebUIPassword            string                 `json:"webuiPassword,omitempty"`
	Debug                    bool                   `json:"debug,omitempty"`
	DevMode                  bool                   `json:"devMode,omitempty"`
	LogLevel                 string                 `json:"logLevel,omitempty"`
	MaxRetries               int                    `json:"maxRetries,omitempty"`
	RetryBaseMs              int                    `json:"retryBaseMs,omitempty"`
	RetryMaxMs               int                    `json:"retryMaxMs,omitempty"`
	PersistTokenCache        bool                   `json:"persistTokenCache,omitempty"`
	DefaultCooldownMs        int                    `json:"defaultCooldownMs,omitempty"`
	MaxWaitBeforeErrorMs     int                    `json:"maxWaitBeforeErrorMs,omitempty"`
	MaxAccounts              int                    `json:"maxAccounts,omitempty"`
	GlobalQuotaThreshold     float64                `json:"globalQuotaThreshold,omitempty"`
	RequestThrottlingEnabled bool                   `json:"requestThrottlingEnabled,omitempty"`
	RequestDelayMs           int                    `json:"requestDelayMs,omitempty"`
	RateLimitDedupWindowMs   int                    `json:"rateLimitDedupWindowMs,omitempty"`
	MaxConsecutiveFailures   int                    `json:"maxConsecutiveFailures,omitempty"`
	ExtendedCooldownMs       int                    `json:"extendedCooldownMs,omitempty"`
	MaxCapacityRetries       int                    `json:"maxCapacityRetries,omitempty"`
	SwitchAccountDelayMs     int                    `json:"switchAccountDelayMs,omitempty"`
	CapacityBackoffTiersMs   []int                  `json:"capacityBackoffTiersMs,omitempty"`
	CustomEndpoints          map[string]EndpointConfig `json:"customEndpoints,omitempty"`
	ModelMapping             map[string]any         `json:"modelMapping,omitempty"`
	AccountSelection         AccountSelectionConfig `json:"accountSelection,omitempty"`
}

var (
	mu            sync.RWMutex
	currentConfig Config
)

// DefaultConfig returns default proxy configuration.
func DefaultConfig() Config {
	return Config{
		LogLevel:               "info",
		MaxRetries:             5,
		RetryBaseMs:            1000,
		RetryMaxMs:             30000,
		DefaultCooldownMs:      10000,
		MaxWaitBeforeErrorMs:   120000,
		MaxAccounts:            10,
		GlobalQuotaThreshold:   0,
		RequestDelayMs:         200,
		RateLimitDedupWindowMs: 2000,
		MaxConsecutiveFailures: 3,
		ExtendedCooldownMs:     60000,
		MaxCapacityRetries:     5,
		SwitchAccountDelayMs:   5000,
		CapacityBackoffTiersMs: []int{5000, 10000, 20000, 30000, 60000},
		CustomEndpoints:        make(map[string]EndpointConfig),
		ModelMapping:           make(map[string]any),
		AccountSelection: AccountSelectionConfig{
			Strategy: "hybrid",
			HealthScore: map[string]any{
				"initial": 70, "successReward": 1, "rateLimitPenalty": -10,
				"failurePenalty": -20, "recoveryPerHour": 10, "minUsable": 50, "maxScore": 100,
			},
			TokenBucket: map[string]any{
				"maxTokens": 50, "tokensPerMinute": 6, "initialTokens": 50,
			},
			Quota: map[string]any{
				"lowThreshold": 0.10, "criticalThreshold": 0.05, "staleMs": 300000,
			},
			Weights: map[string]any{
				"health": 2, "tokens": 5, "quota": 3, "lru": 0.1,
			},
		},
	}
}

// GetConfigDir returns path to ~/.config/antigravity-proxy directory.
func GetConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "antigravity-proxy")
	}
	return filepath.Join(home, ".config", "antigravity-proxy")
}

// ConfigFilePath returns path to ~/.config/antigravity-proxy/config.json.
func ConfigFilePath() (string, error) {
	return filepath.Join(GetConfigDir(), "config.json"), nil
}

// Load reads and parses config.json, falling back to defaults if not found.
func Load() (Config, error) {
	mu.Lock()
	defer mu.Unlock()

	cfg := DefaultConfig()
	path, err := ConfigFilePath()
	if err != nil {
		currentConfig = cfg
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			currentConfig = cfg
			return cfg, nil
		}
		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	currentConfig = cfg
	return cfg, nil
}

// Get returns the in-memory configuration copy.
func Get() Config {
	mu.RLock()
	defer mu.RUnlock()
	return currentConfig
}

// Save writes config to ~/.config/antigravity-proxy/config.json.
func Save(updates map[string]any) (Config, error) {
	mu.Lock()
	defer mu.Unlock()

	path, err := ConfigFilePath()
	if err != nil {
		return currentConfig, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return currentConfig, fmt.Errorf("create config dir: %w", err)
	}

	// Read existing raw JSON map or start with defaults
	var currentMap map[string]any
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &currentMap)
	}
	if currentMap == nil {
		defaultData, _ := json.Marshal(DefaultConfig())
		_ = json.Unmarshal(defaultData, &currentMap)
	}

	// Merge updates
	for k, v := range updates {
		if k == "modelMapping" || k == "customEndpoints" {
			currentMap[k] = v
			continue
		}
		if vMap, ok := v.(map[string]any); ok {
			if existingMap, ok := currentMap[k].(map[string]any); ok {
				for vk, vv := range vMap {
					existingMap[vk] = vv
				}
				currentMap[k] = existingMap
				continue
			}
		}
		currentMap[k] = v
	}

	encoded, err := json.MarshalIndent(currentMap, "", "  ")
	if err != nil {
		return currentConfig, fmt.Errorf("marshal config: %w", err)
	}

	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, encoded, 0600); err != nil {
		return currentConfig, fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmpFile, path); err != nil {
		return currentConfig, fmt.Errorf("rename config: %w", err)
	}

	var updatedConfig Config
	if err := json.Unmarshal(encoded, &updatedConfig); err == nil {
		currentConfig = updatedConfig
	}

	return currentConfig, nil
}

// GetPublicConfig returns config with sensitive fields redacted or safe for UI.
func GetPublicConfig() map[string]any {
	mu.RLock()
	defer mu.RUnlock()

	data, _ := json.Marshal(currentConfig)
	var result map[string]any
	_ = json.Unmarshal(data, &result)

	// Don't expose plaintext password to GET /api/config
	if currentConfig.WebUIPassword != "" {
		result["hasPassword"] = true
	} else {
		result["hasPassword"] = false
	}
	delete(result, "webuiPassword")

	if ce, ok := result["customEndpoints"].(map[string]any); ok {
		redacted := make(map[string]any)
		for model, ep := range ce {
			if epMap, ok := ep.(map[string]any); ok {
				epCopy := make(map[string]any)
				for ek, ev := range epMap {
					if ek == "apiKey" {
						if strKey, isStr := ev.(string); isStr && strKey != "" {
							epCopy["hasApiKey"] = true
						}
					} else {
						epCopy[ek] = ev
					}
				}
				redacted[model] = epCopy
			} else {
				redacted[model] = ep
			}
		}
		result["customEndpoints"] = redacted
	}

	return result
}
