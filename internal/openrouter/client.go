package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ModelItem represents a single OpenRouter model in the catalog.
type ModelItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ContextLength int    `json:"context_length"`
}

// ModelsResponse represents the response format of OpenRouter's GET /v1/models.
type ModelsResponse struct {
	Data []ModelItem `json:"data"`
}

// Client manages OpenRouter catalog discovery and caching.
type Client struct {
	httpClient *http.Client
	mu         sync.RWMutex
	cache      []ModelItem
	cachedAt   time.Time
	cacheTTL   time.Duration
}

// DefaultClient is a shared package-level client instance.
var DefaultClient = NewClient(15*time.Second, 1*time.Hour)

// NewClient initializes a new OpenRouter client with configurable timeout and cache TTL.
func NewClient(timeout time.Duration, cacheTTL time.Duration) *Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if cacheTTL <= 0 {
		cacheTTL = 1 * time.Hour
	}
	return &Client{
		httpClient: &http.Client{Timeout: timeout},
		cacheTTL:   cacheTTL,
	}
}

// NormalizeBaseURL strips trailing slashes and version segments to obtain base API URL.
func NormalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return strings.TrimRight(baseURL, "/")
}

// FetchAvailableModels queries GET <baseURL>/v1/models with the given API key.
func (c *Client) FetchAvailableModels(ctx context.Context, apiKey, baseURL string) ([]ModelItem, error) {
	cleanBase := NormalizeBaseURL(baseURL)
	url := cleanBase + "/v1/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models from openrouter: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var parsed ModelsResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, fmt.Errorf("decode openrouter models json: %w", err)
	}

	c.SaveCache(parsed.Data)
	return parsed.Data, nil
}

// SaveCache updates the in-memory models cache.
func (c *Client) SaveCache(models []ModelItem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make([]ModelItem, len(models))
	copy(c.cache, models)
	c.cachedAt = time.Now()
}

// GetCachedModels returns a copy of currently cached models.
func (c *Client) GetCachedModels() []ModelItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.cache) == 0 {
		return nil
	}
	result := make([]ModelItem, len(c.cache))
	copy(result, c.cache)
	return result
}

// IsCacheValid reports whether the in-memory cache has valid non-expired models.
func (c *Client) IsCacheValid() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache) > 0 && !c.cachedAt.IsZero() && time.Since(c.cachedAt) < c.cacheTTL
}
