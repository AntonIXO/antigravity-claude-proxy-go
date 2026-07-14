package accounts

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadIsReadOnlyAndMatchesNodeStartupReset(t *testing.T) {
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

func testAccount(email string) *Account {
	return &Account{Email: email, Source: "manual", Enabled: true, APIKey: "token-" + email, ProjectID: "project-" + email}
}
