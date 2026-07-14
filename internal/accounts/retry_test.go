package accounts

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
)

func TestParseResetTimeAndClassifiers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header http.Header
		body   string
		want   time.Duration
	}{
		{name: "retry seconds", header: http.Header{"Retry-After": {"12"}}, want: 12 * time.Second},
		{name: "retry date", header: http.Header{"Retry-After": {now.Add(45 * time.Second).Format(http.TimeFormat)}}, want: 45 * time.Second},
		{name: "unix reset", header: http.Header{"X-Ratelimit-Reset": {fmt.Sprint(now.Add(time.Minute).Unix())}}, want: time.Minute},
		{name: "quota delay milliseconds", body: `{"quotaResetDelay":"2500ms"}`, want: 2500 * time.Millisecond},
		{name: "quota delay seconds", body: `quotaResetDelay: 3.5s`, want: 3500 * time.Millisecond},
		{name: "human duration", body: `please retry in 1h2m3s`, want: time.Hour + 2*time.Minute + 3*time.Second},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseResetTime(test.header, test.body, now); got != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
	if ClassifyError(`RESOURCE_EXHAUSTED quotaResetDelay`, 429) != ReasonQuota {
		t.Fatal("quota response was not classified as quota")
	}
	if ClassifyError(`MODEL_CAPACITY_EXHAUSTED`, 429) != ReasonCapacity {
		t.Fatal("capacity response was not classified as capacity")
	}
	if !IsPermanentAuthFailure(`{"error":"invalid_grant"}`) || !IsAccountBanned("Account has been disabled for violation of Terms of Service") {
		t.Fatal("permanent failure classifiers did not match")
	}
	body := `{"error":{"status":"PERMISSION_DENIED","details":[{"metadata":{"reason":"VALIDATION_REQUIRED","validation_url":"https://accounts.google.com/signin/continue?x=1"}}]}}`
	if !IsValidationRequired(body) || ExtractVerificationURL(body) != "https://accounts.google.com/signin/continue?x=1" {
		t.Fatalf("verification parsing failed: %q", ExtractVerificationURL(body))
	}
}

type staticResolver struct {
	tokens      map[string]string
	invalidated []string
	mu          sync.Mutex
}

func (resolver *staticResolver) Resolve(_ context.Context, account *Account) (auth.Credentials, error) {
	token := resolver.tokens[account.Email]
	if token == "" {
		return auth.Credentials{}, fmt.Errorf("missing token for %s", account.Email)
	}
	return auth.Credentials{AccessToken: token, Email: account.Email, Expiry: time.Now().Add(time.Hour)}, nil
}

func (resolver *staticResolver) Invalidate(email string) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.invalidated = append(resolver.invalidated, email)
}

type scriptedResult struct {
	events [][]byte
	err    error
}

type scriptedClient struct {
	mu      sync.Mutex
	results []scriptedResult
	calls   int
	payload map[string]any
}

func (client *scriptedClient) LoadCodeAssist(context.Context, string) (cloudcode.Response, error) {
	return cloudcode.Response{StatusCode: http.StatusOK, Body: []byte(`{"cloudaicompanionProject":{"id":"discovered"}}`)}, nil
}

func (client *scriptedClient) FetchAvailableModels(context.Context, string) (cloudcode.Response, error) {
	return cloudcode.Response{StatusCode: http.StatusOK, Body: []byte(`{"models":{}}`)}, nil
}

func (client *scriptedClient) StreamGenerateContent(_ context.Context, payload any, _ cloudcode.RequestOptions, consume func(cloudcode.SSEEvent) error) (cloudcode.Response, error) {
	client.mu.Lock()
	index := client.calls
	client.calls++
	if object, ok := payload.(map[string]any); ok {
		client.payload = object
	}
	result := client.results[min(index, len(client.results)-1)]
	client.mu.Unlock()
	for _, data := range result.events {
		if err := consume(cloudcode.SSEEvent{Data: data}); err != nil {
			return cloudcode.Response{StatusCode: http.StatusOK}, err
		}
	}
	return cloudcode.Response{Endpoint: cloudcode.DailyEndpoint, StatusCode: http.StatusOK}, result.err
}

func TestForcedQuota429CoolsDownAndRotates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	first := testAccount("first@example.com")
	second := testAccount("second@example.com")
	manager, err := New(Options{Accounts: []*Account{first, second}, Strategy: StrategySticky, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &staticResolver{tokens: map[string]string{first.Email: "first-token", second.Email: "second-token"}}
	quotaError := &cloudcode.HTTPError{
		Endpoint: cloudcode.DailyEndpoint, StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests",
		Header: http.Header{"Retry-After": {"60"}}, Body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"quota exhausted"}}`,
	}
	clients := map[string]*scriptedClient{
		"first-token":  {results: []scriptedResult{{err: quotaError}}},
		"second-token": {results: []scriptedResult{{events: [][]byte{[]byte(`{"response":{"candidates":[]}}`)}}}},
	}
	var sleeps []time.Duration
	dispatcher := newTestDispatcher(t, manager, resolver, clients, now, func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	})
	var events int
	response, err := dispatcher.StreamGenerateContent(context.Background(), testRequest(), func(cloudcode.SSEEvent) error {
		events++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || clients["first-token"].calls != 1 || clients["second-token"].calls != 1 || events != 1 {
		t.Fatalf("response=%#v first=%d second=%d events=%d", response, clients["first-token"].calls, clients["second-token"].calls, events)
	}
	limit := manager.Snapshot()[0].Limits["claude-sonnet-4-6"]
	if !limit.IsRateLimited || limit.ActualResetMS != time.Minute.Milliseconds() {
		t.Fatalf("first account cooldown=%#v", limit)
	}
	if len(sleeps) != 1 || sleeps[0] != 5*time.Second {
		t.Fatalf("switch sleeps=%v", sleeps)
	}
	if clients["second-token"].payload["project"] != second.ProjectID {
		t.Fatalf("rotated payload project=%v", clients["second-token"].payload["project"])
	}
}

func TestCapacityRetriesSameAccountWithTieredBackoff(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	account := testAccount("capacity@example.com")
	manager, err := New(Options{Accounts: []*Account{account}, Strategy: StrategySticky, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &staticResolver{tokens: map[string]string{account.Email: "token"}}
	capacityError := &cloudcode.HTTPError{StatusCode: http.StatusTooManyRequests, Status: "429", Body: `{"error":"MODEL_CAPACITY_EXHAUSTED"}`, Header: make(http.Header)}
	client := &scriptedClient{results: []scriptedResult{{err: capacityError}, {err: capacityError}, {events: [][]byte{[]byte(`{}`)}}}}
	var sleeps []time.Duration
	dispatcher := newTestDispatcher(t, manager, resolver, map[string]*scriptedClient{"token": client}, now, func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	})
	dispatcher.capacityBackoffs = []time.Duration{time.Second, 2 * time.Second}
	if _, err := dispatcher.StreamGenerateContent(context.Background(), testRequest(), func(cloudcode.SSEEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if client.calls != 3 || !reflect.DeepEqual(sleeps, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("calls=%d sleeps=%v", client.calls, sleeps)
	}
	if len(manager.Snapshot()[0].Limits) != 0 {
		t.Fatalf("successful capacity retry left a cooldown: %#v", manager.Snapshot()[0])
	}
}

func TestVerificationAndPermanentAuthFailuresInvalidateAndRotate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		failure    *cloudcode.HTTPError
		wantReason string
		wantURL    string
		wantToken  bool
	}{
		{
			name: "verification", failure: &cloudcode.HTTPError{StatusCode: http.StatusForbidden, Status: "403", Body: `{"error":{"status":"PERMISSION_DENIED","message":"VALIDATION_REQUIRED","details":[{"metadata":{"validation_url":"https://accounts.google.com/signin/continue?x=1"}}]}}`},
			wantReason: "Account requires verification", wantURL: "https://accounts.google.com/signin/continue?x=1",
		},
		{
			name: "tos ban", failure: &cloudcode.HTTPError{StatusCode: http.StatusForbidden, Status: "403", Body: `The account has been disabled for violation of Terms of Service`},
			wantReason: "Account banned — Gemini disabled for Terms of Service violation",
		},
		{
			name: "revoked token", failure: &cloudcode.HTTPError{StatusCode: http.StatusUnauthorized, Status: "401", Body: `{"error":"invalid_grant: token revoked"}`},
			wantReason: "Token revoked - re-authentication required", wantToken: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
			first := testAccount("bad@example.com")
			second := testAccount("good@example.com")
			manager, err := New(Options{Accounts: []*Account{first, second}, Strategy: StrategySticky, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			resolver := &staticResolver{tokens: map[string]string{first.Email: "bad", second.Email: "good"}}
			clients := map[string]*scriptedClient{
				"bad":  {results: []scriptedResult{{err: test.failure}}},
				"good": {results: []scriptedResult{{events: [][]byte{[]byte(`{}`)}}}},
			}
			dispatcher := newTestDispatcher(t, manager, resolver, clients, now, func(context.Context, time.Duration) error { return nil })
			if _, err := dispatcher.StreamGenerateContent(context.Background(), testRequest(), func(cloudcode.SSEEvent) error { return nil }); err != nil {
				t.Fatal(err)
			}
			snapshot := manager.Snapshot()[0]
			if !snapshot.Invalid || snapshot.InvalidReason != test.wantReason || snapshot.VerifyURL != test.wantURL {
				t.Fatalf("snapshot=%#v", snapshot)
			}
			resolver.mu.Lock()
			invalidated := append([]string(nil), resolver.invalidated...)
			resolver.mu.Unlock()
			if test.wantToken != reflect.DeepEqual(invalidated, []string{first.Email}) {
				t.Fatalf("invalidated tokens=%v", invalidated)
			}
		})
	}
}

func newTestDispatcher(t *testing.T, manager *Manager, resolver Resolver, clients map[string]*scriptedClient, now time.Time, sleep SleepFunc) *Dispatcher {
	t.Helper()
	dispatcher, err := NewDispatcher(DispatcherOptions{
		Manager: manager, Resolver: resolver, MaxRetries: 5, Sleep: sleep, Now: func() time.Time { return now },
		NewClient: func(token string) CloudClient {
			client := clients[token]
			if client == nil {
				t.Fatalf("unexpected token %q", token)
			}
			return client
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func testRequest() map[string]any {
	return map[string]any{
		"model": "claude-sonnet-4-6", "max_tokens": float64(128),
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
}
