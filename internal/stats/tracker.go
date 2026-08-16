package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ModelMetrics tracks request count, total latency, and token metrics for a model.
type ModelMetrics struct {
	Requests        int   `json:"requests"`
	LatencyMS       int64 `json:"latency_ms"`
	InputTokens     int   `json:"input_tokens"`
	OutputTokens    int   `json:"output_tokens"`
	CacheReadTokens int   `json:"cache_read_tokens"`
}

// Tracker manages hourly request volume statistics by model and family with disk persistence.
type Tracker struct {
	mu           sync.RWMutex
	filePath     string
	history      map[string]map[string]any
	dirty        bool
	stopAutoSave func()
	wg           sync.WaitGroup
}

// HourlyBucketKey formats time into an ISO8601 string rounded down to the hour.
func HourlyBucketKey(t time.Time) string {
	return t.UTC().Truncate(time.Hour).Format("2006-01-02T15:00:00.000Z")
}

// ResolveModelFamilyAndShortName determines the family ('claude', 'gemini', 'other')
// and extracts the short name by removing the family prefix if present.
func ResolveModelFamilyAndShortName(modelID string) (string, string) {
	lower := strings.ToLower(modelID)
	var family string
	if strings.Contains(lower, "claude") {
		family = "claude"
	} else if strings.Contains(lower, "gemini") {
		family = "gemini"
	} else {
		return "other", modelID
	}

	prefix := family + "-"
	if strings.HasPrefix(lower, prefix) {
		return family, modelID[len(prefix):]
	}
	return family, modelID
}

// NewTracker initializes a statistics tracker and loads existing history from filePath if available.
func NewTracker(filePath string) (*Tracker, error) {
	t := &Tracker{
		filePath: filePath,
		history:  make(map[string]map[string]any),
	}

	if filePath != "" {
		if data, err := os.ReadFile(filePath); err == nil {
			var raw map[string]any
			if jsonErr := json.Unmarshal(data, &raw); jsonErr == nil {
				t.history = normalizeHistory(raw)
			}
		}
	}

	return t, nil
}

func parseModelMetrics(m map[string]any) ModelMetrics {
	var metrics ModelMetrics
	if r, ok := m["requests"].(float64); ok {
		metrics.Requests = int(r)
	} else if r, ok := m["requests"].(int); ok {
		metrics.Requests = r
	}

	if l, ok := m["latency_ms"].(float64); ok {
		metrics.LatencyMS = int64(l)
	} else if l, ok := m["latency_ms"].(int64); ok {
		metrics.LatencyMS = l
	} else if l, ok := m["latency_ms"].(int); ok {
		metrics.LatencyMS = int64(l)
	}

	if it, ok := m["input_tokens"].(float64); ok {
		metrics.InputTokens = int(it)
	} else if it, ok := m["input_tokens"].(int); ok {
		metrics.InputTokens = it
	}

	if ot, ok := m["output_tokens"].(float64); ok {
		metrics.OutputTokens = int(ot)
	} else if ot, ok := m["output_tokens"].(int); ok {
		metrics.OutputTokens = ot
	}

	if cr, ok := m["cache_read_tokens"].(float64); ok {
		metrics.CacheReadTokens = int(cr)
	} else if cr, ok := m["cache_read_tokens"].(int); ok {
		metrics.CacheReadTokens = cr
	}
	return metrics
}

func normalizeHistory(raw map[string]any) map[string]map[string]any {
	result := make(map[string]map[string]any)
	for hourKey, hourVal := range raw {
		hourMap, ok := hourVal.(map[string]any)
		if !ok {
			continue
		}
		normHour := make(map[string]any)
		for k, v := range hourMap {
			if k == "_total" || k == "total" {
				switch val := v.(type) {
				case float64:
					normHour["_total"] = int(val)
				case int:
					normHour["_total"] = val
				}
			} else if famMap, ok := v.(map[string]any); ok {
				normFam := make(map[string]any)
				for mk, mv := range famMap {
					if mk == "_subtotal" {
						switch val := mv.(type) {
						case float64:
							normFam[mk] = int(val)
						case int:
							normFam[mk] = val
						}
					} else {
						switch val := mv.(type) {
						case float64:
							normFam[mk] = ModelMetrics{Requests: int(val)}
						case int:
							normFam[mk] = ModelMetrics{Requests: val}
						case map[string]any:
							normFam[mk] = parseModelMetrics(val)
						}
					}
				}
				if _, hasSub := normFam["_subtotal"]; !hasSub {
					normFam["_subtotal"] = 0
				}
				normHour[k] = normFam
			}
		}
		if _, hasTotal := normHour["_total"]; !hasTotal {
			normHour["_total"] = 0
		}
		result[hourKey] = normHour
	}
	return result
}

// Track records a request completion for the given model ID.
func (t *Tracker) Track(modelID string) {
	t.TrackRequest(modelID, 0, 0, 0, 0)
}

// TrackRequest records a request completion with latency and token metrics for the given model ID.
func (t *Tracker) TrackRequest(modelID string, latency time.Duration, inputTokens, outputTokens, cacheReadTokens int) {
	if modelID == "" {
		return
	}
	family, shortName := ResolveModelFamilyAndShortName(modelID)
	hourKey := HourlyBucketKey(time.Now())

	t.mu.Lock()
	defer t.mu.Unlock()

	hourMap, exists := t.history[hourKey]
	if !exists {
		hourMap = make(map[string]any)
		hourMap["_total"] = 0
		t.history[hourKey] = hourMap
	}

	famRaw, famExists := hourMap[family]
	var famMap map[string]any
	if famExists {
		famMap, _ = famRaw.(map[string]any)
	}
	if famMap == nil {
		famMap = make(map[string]any)
		famMap["_subtotal"] = 0
		hourMap[family] = famMap
	}

	var metrics ModelMetrics
	if existing, ok := famMap[shortName]; ok {
		switch m := existing.(type) {
		case ModelMetrics:
			metrics = m
		case map[string]any:
			metrics = parseModelMetrics(m)
		case int:
			metrics.Requests = m
		case float64:
			metrics.Requests = int(m)
		}
	}
	metrics.Requests++
	metrics.LatencyMS += latency.Milliseconds()
	metrics.InputTokens += inputTokens
	metrics.OutputTokens += outputTokens
	metrics.CacheReadTokens += cacheReadTokens

	famMap[shortName] = metrics

	subtotal, _ := famMap["_subtotal"].(int)
	famMap["_subtotal"] = subtotal + 1

	if total, ok := hourMap["_total"].(int); ok {
		hourMap["_total"] = total + 1
	} else {
		hourMap["_total"] = 1
	}

	t.dirty = true
}

// GetHistory returns a deep clone of the tracked usage history.
func (t *Tracker) GetHistory() map[string]any {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.getHistoryLocked()
}

func (t *Tracker) getHistoryLocked() map[string]any {
	result := make(map[string]any, len(t.history))
	for hourKey, hourMap := range t.history {
		hourCopy := make(map[string]any, len(hourMap))
		for k, v := range hourMap {
			if k == "_total" {
				hourCopy[k] = v
			} else if famMap, ok := v.(map[string]any); ok {
				famCopy := make(map[string]any, len(famMap))
				for mk, mv := range famMap {
					famCopy[mk] = mv
				}
				hourCopy[k] = famCopy
			}
		}
		result[hourKey] = hourCopy
	}
	return result
}

// Prune removes hourly buckets older than the given retention period.
func (t *Tracker) Prune(retention time.Duration) {
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-retention)

	t.mu.Lock()
	defer t.mu.Unlock()

	for hourKey := range t.history {
		parsedTime, err := time.Parse(time.RFC3339, hourKey)
		if err != nil {
			parsedTime, err = time.Parse("2006-01-02T15:00:00.000Z", hourKey)
		}
		if err == nil && parsedTime.Before(cutoff) {
			delete(t.history, hourKey)
			t.dirty = true
		}
	}
}

// Save writes the usage history to disk atomically if changes were made.
func (t *Tracker) Save() error {
	t.mu.Lock()
	if !t.dirty {
		t.mu.Unlock()
		return nil
	}
	if t.filePath == "" {
		t.mu.Unlock()
		return nil
	}

	data := t.getHistoryLocked()
	t.dirty = false
	t.mu.Unlock()

	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(t.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmpFile := t.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, bytes, 0644); err != nil {
		return err
	}

	return os.Rename(tmpFile, t.filePath)
}

// StartAutoSave starts a background goroutine that periodically saves and prunes history.
func (t *Tracker) StartAutoSave(interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	stopCh := make(chan struct{})

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			select {
			case <-ticker.C:
				t.Prune(30 * 24 * time.Hour)
				_ = t.Save()
			case <-stopCh:
				ticker.Stop()
				return
			}
		}
	}()

	var once sync.Once
	stopFunc := func() {
		once.Do(func() {
			close(stopCh)
		})
	}

	t.mu.Lock()
	t.stopAutoSave = stopFunc
	t.mu.Unlock()

	return stopFunc
}

// Close stops auto-save if active, prunes, and saves history.
func (t *Tracker) Close() error {
	t.mu.Lock()
	stopFunc := t.stopAutoSave
	t.mu.Unlock()

	if stopFunc != nil {
		stopFunc()
	}
	t.wg.Wait()

	t.Prune(30 * 24 * time.Hour)
	return t.Save()
}
