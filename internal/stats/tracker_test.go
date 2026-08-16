package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHourlyBucketKey(t *testing.T) {
	testTime, err := time.Parse(time.RFC3339, "2026-08-14T20:34:56.789Z")
	if err != nil {
		t.Fatalf("Failed to parse time: %v", err)
	}

	key := HourlyBucketKey(testTime)
	expected := "2026-08-14T20:00:00.000Z"
	if key != expected {
		t.Errorf("HourlyBucketKey() = %s, expected %s", key, expected)
	}
}

func TestResolveModelFamilyAndShortName(t *testing.T) {
	tests := []struct {
		modelID       string
		expectedFam   string
		expectedShort string
	}{
		{"claude-3-5-sonnet-20241022", "claude", "3-5-sonnet-20241022"},
		{"claude-opus-4-6", "claude", "opus-4-6"},
		{"CLAUDE-HAVANA", "claude", "HAVANA"},
		{"gemini-2.5-flash", "gemini", "2.5-flash"},
		{"gemini-pro", "gemini", "pro"},
		{"custom-gpt-4o", "other", "custom-gpt-4o"},
		{"claude", "claude", "claude"},
	}

	for _, tt := range tests {
		fam, short := ResolveModelFamilyAndShortName(tt.modelID)
		if fam != tt.expectedFam || short != tt.expectedShort {
			t.Errorf("ResolveModelFamilyAndShortName(%q) = (%q, %q), expected (%q, %q)",
				tt.modelID, fam, short, tt.expectedFam, tt.expectedShort)
		}
	}
}

func TestTracker_TrackAndGetHistory(t *testing.T) {
	tracker, err := NewTracker("")
	if err != nil {
		t.Fatalf("NewTracker failed: %v", err)
	}

	tracker.Track("claude-3-5-sonnet-20241022")
	tracker.Track("claude-3-5-sonnet-20241022")
	tracker.Track("gemini-2.5-flash")

	history := tracker.GetHistory()
	if len(history) != 1 {
		t.Fatalf("Expected 1 hour bucket in history, got %d", len(history))
	}

	var hourMap map[string]any
	for _, v := range history {
		hourMap = v.(map[string]any)
	}

	if hourMap["_total"] != 3 {
		t.Errorf("Expected _total 3, got %v", hourMap["_total"])
	}

	claudeMap, ok := hourMap["claude"].(map[string]any)
	if !ok {
		t.Fatalf("Expected claude family map, got %v", hourMap["claude"])
	}

	if claudeMap["_subtotal"] != 2 {
		t.Errorf("Expected claude _subtotal 2, got %d", claudeMap["_subtotal"])
	}
	claudeMetrics, ok := claudeMap["3-5-sonnet-20241022"].(ModelMetrics)
	if !ok || claudeMetrics.Requests != 2 {
		t.Errorf("Expected claude model count 2, got %v", claudeMap["3-5-sonnet-20241022"])
	}

	geminiMap, ok := hourMap["gemini"].(map[string]any)
	if !ok {
		t.Fatalf("Expected gemini family map, got %v", hourMap["gemini"])
	}
	if geminiMap["_subtotal"] != 1 {
		t.Errorf("Expected gemini _subtotal 1, got %d", geminiMap["_subtotal"])
	}
	geminiMetrics, ok := geminiMap["2.5-flash"].(ModelMetrics)
	if !ok || geminiMetrics.Requests != 1 {
		t.Errorf("Expected gemini model count 1, got %v", geminiMap["2.5-flash"])
	}
}

func TestTracker_ConcurrentTrack(t *testing.T) {
	tracker, err := NewTracker("")
	if err != nil {
		t.Fatalf("NewTracker failed: %v", err)
	}

	const goroutines = 20
	const iterations = 50
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if id%2 == 0 {
					tracker.Track("claude-3-5-sonnet")
				} else {
					tracker.Track("gemini-1.5-pro")
				}
			}
		}(i)
	}

	wg.Wait()

	history := tracker.GetHistory()
	var hourMap map[string]any
	for _, v := range history {
		hourMap = v.(map[string]any)
	}

	expectedTotal := goroutines * iterations
	if hourMap["_total"] != expectedTotal {
		t.Errorf("Expected _total %d, got %v", expectedTotal, hourMap["_total"])
	}
}

func TestTracker_FileSaveAndReload(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "usage-history.json")

	tracker1, err := NewTracker(filePath)
	if err != nil {
		t.Fatalf("NewTracker failed: %v", err)
	}

	tracker1.Track("claude-opus-4-6")
	if err := tracker1.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("Expected history file to exist at %s", filePath)
	}

	tracker2, err := NewTracker(filePath)
	if err != nil {
		t.Fatalf("NewTracker reload failed: %v", err)
	}

	history := tracker2.GetHistory()
	if len(history) != 1 {
		t.Fatalf("Expected 1 hour bucket in reloaded history, got %d", len(history))
	}

	var hourMap map[string]any
	for _, v := range history {
		hourMap = v.(map[string]any)
	}

	if hourMap["_total"] != 1 {
		t.Errorf("Expected _total 1 in reloaded history, got %v", hourMap["_total"])
	}
}

func TestTracker_Prune(t *testing.T) {
	tracker, err := NewTracker("")
	if err != nil {
		t.Fatalf("NewTracker failed: %v", err)
	}

	oldHourKey := "2020-01-01T00:00:00.000Z"
	recentHourKey := HourlyBucketKey(time.Now())

	rawJSON := map[string]any{
		oldHourKey: map[string]any{
			"_total": 5,
			"claude": map[string]any{"_subtotal": 5, "opus": 5},
		},
		recentHourKey: map[string]any{
			"_total": 2,
			"gemini": map[string]any{"_subtotal": 2, "flash": 2},
		},
	}

	data, _ := json.Marshal(rawJSON)
	var parsed map[string]any
	_ = json.Unmarshal(data, &parsed)
	tracker.history = normalizeHistory(parsed)

	tracker.Prune(30 * 24 * time.Hour)

	history := tracker.GetHistory()
	if len(history) != 1 {
		t.Fatalf("Expected 1 bucket remaining after prune, got %d", len(history))
	}
	if _, exists := history[recentHourKey]; !exists {
		t.Errorf("Expected recent hour key %s to exist", recentHourKey)
	}
	if _, exists := history[oldHourKey]; exists {
		t.Errorf("Expected old hour key %s to be pruned", oldHourKey)
	}
}

func TestTracker_TrackRequest(t *testing.T) {
	tr, err := NewTracker("")
	if err != nil {
		t.Fatalf("failed to create tracker: %v", err)
	}
	tr.TrackRequest("claude-3-5-sonnet", 500*time.Millisecond, 100, 50, 20)
	history := tr.GetHistory()
	if len(history) == 0 {
		t.Fatalf("expected non-empty history")
	}

	var hourMap map[string]any
	for _, v := range history {
		hourMap = v.(map[string]any)
	}
	claudeMap, ok := hourMap["claude"].(map[string]any)
	if !ok {
		t.Fatalf("expected claude family map, got %T", hourMap["claude"])
	}
	metrics, ok := claudeMap["3-5-sonnet"].(ModelMetrics)
	if !ok {
		t.Fatalf("expected ModelMetrics for 3-5-sonnet, got %T", claudeMap["3-5-sonnet"])
	}
	if metrics.Requests != 1 || metrics.LatencyMS != 500 || metrics.InputTokens != 100 || metrics.OutputTokens != 50 || metrics.CacheReadTokens != 20 {
		t.Errorf("unexpected metrics values: %+v", metrics)
	}
}
