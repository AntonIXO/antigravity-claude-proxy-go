package accounts

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadIsReadOnlyAndResetsTransientStartupState(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "accounts.json")
	original := []byte(`{
  "activeIndex": 99,
  "settings": {"strategy":"hybrid"},
  "accounts": [
    {"email":"reset@example.com","source":"agy","isInvalid":true,"invalidReason":"old failure"},
    {"email":"verify@example.com","source":"oauth","enabled":false,"isInvalid":true,"invalidReason":"verify","verifyUrl":"https://accounts.google.com/signin/continue?x=1","modelRateLimits":{"claude":{"isRateLimited":true,"resetTime":1234,"actualResetMs":1000}}}
  ]
}`)
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ActiveIndex != 0 || len(loaded.Accounts) != 2 {
		t.Fatalf("loaded=%#v", loaded)
	}
	if !loaded.Accounts[0].Enabled || loaded.Accounts[0].IsInvalid || loaded.Accounts[0].InvalidReason != "" {
		t.Fatalf("startup-reset account=%#v", loaded.Accounts[0])
	}
	if loaded.Accounts[1].Enabled || !loaded.Accounts[1].IsInvalid || loaded.Accounts[1].VerifyURL == "" {
		t.Fatalf("verification account=%#v", loaded.Accounts[1])
	}
	if limit := loaded.Accounts[1].ModelRateLimits["claude"]; limit == nil || limit.ResetTimeMS != 1234 {
		t.Fatalf("rate limit=%#v", limit)
	}
	afterContents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterContents, original) || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("Load changed the account file: mode %v -> %v, mtime %v -> %v", before.Mode(), after.Mode(), before.ModTime(), after.ModTime())
	}
}

func TestNewDefaultUsesActiveAgyLoginWithoutAccountFile(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	path := filepath.Join(directory, "antigravity-oauth-token")
	if err := os.WriteFile(path, []byte(`{"token":{"access_token":"token","expiry":"2030-01-01T00:00:00Z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_TOKEN_PATH", path)
	manager, err := NewDefault("", StrategyHybrid, nil)
	if err != nil {
		t.Fatal(err)
	}
	selection := manager.Select("gemini")
	if selection.Account == nil || selection.Account.Source != "agy" || selection.Account.AgyTokenPath != path {
		t.Fatalf("selection = %#v", selection)
	}
}

func TestRoundRobinAndPerModelRateLimits(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	first := testAccount("first@example.com")
	second := testAccount("second@example.com")
	manager, err := New(Options{Accounts: []*Account{first, second}, Strategy: StrategyRoundRobin, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	selection := manager.Select("claude-sonnet-4-6")
	if selection.Account != second {
		t.Fatalf("first round-robin selection=%v", selection.Account.Email)
	}
	manager.MarkRateLimited(second, "claude-sonnet-4-6", time.Minute)
	if got := manager.Select("claude-sonnet-4-6").Account; got != first {
		t.Fatalf("Claude selection=%v", got)
	}
	if got := manager.Select("gemini-3.5-flash-low").Account; got != second {
		t.Fatalf("model-specific limit leaked to Gemini: selection=%v", got)
	}
	now = now.Add(time.Minute + time.Millisecond)
	if manager.Available("claude-sonnet-4-6") != 2 {
		t.Fatal("expired per-model limit was not cleared")
	}
}

func TestStickyWaitsOnlyForShortCurrentLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	account := testAccount("sticky@example.com")
	manager, err := New(Options{Accounts: []*Account{account}, Strategy: StrategySticky, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	manager.MarkRateLimited(account, "claude", 30*time.Second)
	selection := manager.Select("claude")
	if selection.Account != nil || selection.Wait != 30*time.Second {
		t.Fatalf("selection=%#v", selection)
	}
	manager.MarkRateLimited(account, "claude", 3*time.Minute)
	if selection := manager.Select("claude"); selection.Account != nil || selection.Wait != 0 {
		t.Fatalf("long cooldown should rotate/fail without waiting: %#v", selection)
	}
}

func TestMarkFailureDoesNotRateLimitEmptyModel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	account := testAccount("empty-model@example.com")
	manager, err := New(Options{Accounts: []*Account{account}, Strategy: StrategyHybrid, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		manager.MarkFailure(account, "")
	}
	if account.IsInvalid || account.ModelRateLimits[""] != nil {
		t.Fatalf("empty-model MarkFailure created a rate limit or invalidation: IsInvalid=%v ModelRateLimits[\"\"]=%#v", account.IsInvalid, account.ModelRateLimits[""])
	}

	account.ConsecutiveFailure = 0
	for i := 0; i < 2; i++ {
		manager.MarkFailure(account, "claude")
	}
	if account.ModelRateLimits["claude"] != nil {
		t.Fatalf("rate limit created before 3 consecutive failures: %#v", account.ModelRateLimits["claude"])
	}
	manager.MarkFailure(account, "claude")
	if limit := account.ModelRateLimits["claude"]; limit == nil || !limit.IsRateLimited {
		t.Fatalf("expected per-model rate limit after 3 consecutive failures, got %#v", limit)
	}
}

func TestAccountCloningAndConcurrency(t *testing.T) {
	t.Parallel()
	account := testAccount("concurrent@example.com")
	account.ModelRateLimits = make(map[string]*RateLimit)
	account.ModelThreshold = make(map[string]float64)
	account.Quota.Models = make(map[string]ModelQuota)
	account.ModelRateLimits["claude"] = &RateLimit{IsRateLimited: true, ResetTimeMS: 1234}
	account.ModelThreshold["claude"] = 0.1
	quarter := 0.25
	account.Quota.Models["claude"] = ModelQuota{RemainingFraction: &quarter}

	manager, err := New(Options{Accounts: []*Account{account}, Strategy: StrategyHybrid})
	if err != nil {
		t.Fatal(err)
	}

	// Verify GetAllAccounts returns cloned objects
	list := manager.GetAllAccounts()
	if len(list) != 1 {
		t.Fatalf("expected 1 account, got %d", len(list))
	}
	if list[0] == account {
		t.Error("GetAllAccounts returned pointer to internal account struct, expected cloned pointer")
	}
	if list[0].ModelRateLimits["claude"] == account.ModelRateLimits["claude"] {
		t.Error("GetAllAccounts did not clone ModelRateLimits map values")
	}

	// Concurrent mutation and GetAllAccounts read should not trigger race detector
	done := make(chan bool)
	go func() {
		for i := 0; i < 100; i++ {
			manager.MarkRateLimited(account, "claude", time.Second)
			manager.MarkSuccess(account, "claude")
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			accs := manager.GetAllAccounts()
			for _, a := range accs {
				_ = a.ModelRateLimits["claude"]
				_ = a.Quota.Models["claude"]
			}
		}
		done <- true
	}()

	<-done
	<-done
}

func TestExtractSubscriptionAndTierDetection(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		jsonBody      string
		wantTier      string
		wantProjectID string
	}{
		{
			name:          "Pro account from paidTier and currentTier",
			jsonBody:      `{"cloudaicompanionProject":"proj-pro","currentTier":{"id":"tier-pro","name":"Google One AI Premium"},"paidTier":{"id":"tier-pro"}}`,
			wantTier:      "pro",
			wantProjectID: "proj-pro",
		},
		{
			name:          "Pro account from g1Tier PRO",
			jsonBody:      `{"cloudaicompanionProject":"proj-g1","currentTier":{"id":"tier-1"},"g1Tier":"PRO"}`,
			wantTier:      "pro",
			wantProjectID: "proj-g1",
		},
		{
			name:          "Ultra account from currentTier name and g1Tier",
			jsonBody:      `{"cloudaicompanionProject":"proj-ultra","currentTier":{"id":"tier-ultra","name":"Gemini Ultra"},"g1Tier":"ULTRA"}`,
			wantTier:      "ultra",
			wantProjectID: "proj-ultra",
		},
		{
			name:          "Free account from currentTier free",
			jsonBody:      `{"cloudaicompanionProject":"proj-free","currentTier":{"id":"tier-free","name":"Free Tier"}}`,
			wantTier:      "free",
			wantProjectID: "proj-free",
		},
		{
			name:          "Project object format",
			jsonBody:      `{"cloudaicompanionProject":{"id":"proj-obj"},"currentTier":{"id":"tier-pro"}}`,
			wantTier:      "pro",
			wantProjectID: "proj-obj",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sub := ExtractSubscription([]byte(tc.jsonBody), now)
			if sub.Tier != tc.wantTier {
				t.Errorf("got Tier = %q, want %q", sub.Tier, tc.wantTier)
			}
			if sub.ProjectID != tc.wantProjectID {
				t.Errorf("got ProjectID = %q, want %q", sub.ProjectID, tc.wantProjectID)
			}
		})
	}
}

func testAccount(email string) *Account {
	return &Account{
		Email:           email,
		Source:          "manual",
		Enabled:         true,
		APIKey:          "token-" + email,
		ProjectID:       "project-" + email,
		ModelRateLimits: make(map[string]*RateLimit),
		ModelThreshold:  make(map[string]float64),
		Quota:           Quota{Models: make(map[string]ModelQuota)},
	}
}
