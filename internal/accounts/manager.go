package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"antigravity-go-proxy/internal/auth"
)

const (
	StrategySticky     = "sticky"
	StrategyRoundRobin = "round-robin"
	StrategyHybrid     = "hybrid"
	DefaultStrategy    = StrategyHybrid
	maxStickyWait      = 2 * time.Minute
)

type RateLimit struct {
	IsRateLimited bool  `json:"isRateLimited"`
	ResetTimeMS   int64 `json:"-"`
	ActualResetMS int64 `json:"actualResetMs,omitempty"`
}

func (limit *RateLimit) UnmarshalJSON(data []byte) error {
	var raw struct {
		IsRateLimited bool            `json:"isRateLimited"`
		ResetTime     json.RawMessage `json:"resetTime"`
		ActualResetMS int64           `json:"actualResetMs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	limit.IsRateLimited = raw.IsRateLimited
	limit.ActualResetMS = raw.ActualResetMS
	if len(raw.ResetTime) > 0 && string(raw.ResetTime) != "null" {
		var number float64
		if err := json.Unmarshal(raw.ResetTime, &number); err == nil {
			limit.ResetTimeMS = int64(number)
		}
	}
	return nil
}

type ModelQuota struct {
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime,omitempty"`
}

type Quota struct {
	Models      map[string]ModelQuota `json:"models"`
	LastChecked any                   `json:"lastChecked"`
}

type Subscription struct {
	Tier       string `json:"tier"`
	ProjectID  string `json:"projectId"`
	DetectedAt string `json:"detectedAt"`
}

type Account struct {
	Email          string
	Source         string
	Enabled        bool
	DBPath         string
	RefreshToken   string
	APIKey         string
	AgyTokenPath   string
	ProjectID      string
	Subscription   Subscription
	Quota          Quota
	QuotaThreshold *float64
	ModelThreshold map[string]float64

	LastUsedMS         int64
	IsInvalid          bool
	InvalidReason      string
	VerifyURL          string
	ModelRateLimits    map[string]*RateLimit
	ConsecutiveFailure int
	CoolingDownUntilMS int64
	CooldownReason     string
}

type diskAccount struct {
	Email                string                `json:"email"`
	Source               string                `json:"source"`
	Enabled              *bool                 `json:"enabled"`
	DBPath               string                `json:"dbPath"`
	RefreshToken         string                `json:"refreshToken"`
	APIKey               string                `json:"apiKey"`
	AgyTokenPath         string                `json:"agyTokenPath"`
	ProjectID            string                `json:"projectId"`
	Subscription         Subscription          `json:"subscription"`
	Quota                Quota                 `json:"quota"`
	QuotaThreshold       *float64              `json:"quotaThreshold"`
	ModelQuotaThresholds map[string]float64    `json:"modelQuotaThresholds"`
	LastUsed             json.RawMessage       `json:"lastUsed"`
	IsInvalid            bool                  `json:"isInvalid"`
	InvalidReason        string                `json:"invalidReason"`
	VerifyURL            string                `json:"verifyUrl"`
	ModelRateLimits      map[string]*RateLimit `json:"modelRateLimits"`
}

type File struct {
	Accounts    []*Account
	Settings    map[string]any
	ActiveIndex int
}

func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "antigravity-proxy", "accounts.json"), nil
}

// Load reads the account-pool configuration without ever opening it for
// writing. Invalid state is reset on startup unless a verification URL requires
// user action.
func Load(path string) (File, error) {
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return File{}, err
		}
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read account configuration: %w", err)
	}
	var document struct {
		Accounts    []diskAccount  `json:"accounts"`
		Settings    map[string]any `json:"settings"`
		ActiveIndex int            `json:"activeIndex"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		return File{}, fmt.Errorf("decode account configuration: %w", err)
	}
	result := File{Settings: document.Settings, ActiveIndex: document.ActiveIndex}
	for _, stored := range document.Accounts {
		enabled := stored.Enabled == nil || *stored.Enabled
		account := &Account{
			Email: stored.Email, Source: stored.Source, Enabled: enabled,
			DBPath: stored.DBPath, RefreshToken: stored.RefreshToken, APIKey: stored.APIKey,
			AgyTokenPath: stored.AgyTokenPath, ProjectID: stored.ProjectID,
			Subscription: stored.Subscription, Quota: stored.Quota,
			QuotaThreshold: stored.QuotaThreshold, ModelThreshold: stored.ModelQuotaThresholds,
			VerifyURL: stored.VerifyURL, ModelRateLimits: stored.ModelRateLimits,
		}
		if account.Source == "" {
			account.Source = "database"
		}
		if account.ModelRateLimits == nil {
			account.ModelRateLimits = make(map[string]*RateLimit)
		}
		if account.ModelThreshold == nil {
			account.ModelThreshold = make(map[string]float64)
		}
		if account.Quota.Models == nil {
			account.Quota.Models = make(map[string]ModelQuota)
		}
		if stored.VerifyURL != "" {
			account.IsInvalid = stored.IsInvalid
			account.InvalidReason = stored.InvalidReason
		}
		account.LastUsedMS = parseMilliseconds(stored.LastUsed)
		result.Accounts = append(result.Accounts, account)
	}
	if result.ActiveIndex < 0 || result.ActiveIndex >= len(result.Accounts) {
		result.ActiveIndex = 0
	}
	return result, nil
}

type Options struct {
	Accounts    []*Account
	ActiveIndex int
	Strategy    string
	Now         func() time.Time
}

type Selection struct {
	Account *Account
	Wait    time.Duration
}

type healthRecord struct {
	Score       float64
	LastUpdated time.Time
}

type tokenBucket struct {
	Tokens      float64
	LastUpdated time.Time
}

type Manager struct {
	mu           sync.Mutex
	accounts     []*Account
	strategy     string
	currentIndex int
	cursor       int
	now          func() time.Time
	health       map[string]healthRecord
	buckets      map[string]tokenBucket
	projects     map[string]string
}

func New(options Options) (*Manager, error) {
	strategy := normalizeStrategy(options.Strategy)
	if options.Now == nil {
		options.Now = time.Now
	}
	if len(options.Accounts) == 0 {
		return nil, errors.New("no accounts configured")
	}
	for _, account := range options.Accounts {
		if account == nil {
			return nil, errors.New("account configuration contains a null account")
		}
		if account.Source == "" {
			account.Source = "database"
		}
		if account.ModelRateLimits == nil {
			account.ModelRateLimits = make(map[string]*RateLimit)
		}
		if account.ModelThreshold == nil {
			account.ModelThreshold = make(map[string]float64)
		}
		if account.Quota.Models == nil {
			account.Quota.Models = make(map[string]ModelQuota)
		}
	}
	if options.ActiveIndex < 0 || options.ActiveIndex >= len(options.Accounts) {
		options.ActiveIndex = 0
	}
	return &Manager{
		accounts: options.Accounts, strategy: strategy, currentIndex: options.ActiveIndex,
		now: options.Now, health: make(map[string]healthRecord),
		buckets: make(map[string]tokenBucket), projects: make(map[string]string),
	}, nil
}

func NewFromFile(path, strategy string, now func() time.Time) (*Manager, error) {
	file, err := Load(path)
	if err != nil {
		return nil, err
	}
	return New(Options{Accounts: file.Accounts, ActiveIndex: file.ActiveIndex, Strategy: strategy, Now: now})
}

// NewDefault uses the optional account-pool configuration when it exists.
// Otherwise it creates a one-account pool from the active agy login, so a
// normal logged-in CLI requires no proxy-specific account configuration.
func NewDefault(path, strategy string, now func() time.Time) (*Manager, error) {
	if path != "" {
		return NewFromFile(path, strategy, now)
	}
	configPath, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(configPath); err == nil {
		return NewFromFile(configPath, strategy, now)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect account configuration: %w", err)
	}
	tokenPath, err := auth.DefaultTokenPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(tokenPath); err != nil {
		return nil, fmt.Errorf("no account configuration and no agy login token at %q: %w", tokenPath, err)
	}
	return New(Options{
		Accounts: []*Account{{Email: "agy", Source: "agy", Enabled: true, AgyTokenPath: tokenPath}},
		Strategy: strategy,
		Now:      now,
	})
}

func (manager *Manager) Count() int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return len(manager.accounts)
}

func (manager *Manager) Select(model string) Selection {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.clearExpiredLocked()
	switch manager.strategy {
	case StrategySticky:
		return manager.selectStickyLocked(model)
	case StrategyRoundRobin:
		return manager.selectRoundRobinLocked(model)
	default:
		return manager.selectHybridLocked(model)
	}
}

func (manager *Manager) Available(model string) int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.clearExpiredLocked()
	count := 0
	for _, account := range manager.accounts {
		if manager.usableLocked(account, model) {
			count++
		}
	}
	return count
}

func (manager *Manager) AllInvalid() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	enabled := 0
	invalid := 0
	for _, account := range manager.accounts {
		if account.Enabled {
			enabled++
			if account.IsInvalid {
				invalid++
			}
		}
	}
	return enabled > 0 && enabled == invalid
}

func (manager *Manager) MinWait(model string) time.Duration {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := manager.now().UnixMilli()
	minimum := int64(0)
	for _, account := range manager.accounts {
		if limit := account.ModelRateLimits[model]; limit != nil && limit.IsRateLimited && limit.ResetTimeMS > now {
			wait := limit.ResetTimeMS - now
			if minimum == 0 || wait < minimum {
				minimum = wait
			}
		}
	}
	return time.Duration(minimum) * time.Millisecond
}

func (manager *Manager) MarkRateLimited(account *Account, model string, wait time.Duration) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if wait <= 0 {
		wait = 10 * time.Second
	}
	account.ModelRateLimits[model] = &RateLimit{
		IsRateLimited: true, ResetTimeMS: manager.now().Add(wait).UnixMilli(), ActualResetMS: wait.Milliseconds(),
	}
	account.ConsecutiveFailure++
	manager.recordRateLimitLocked(account.Email)
}

func (manager *Manager) MarkInvalid(account *Account, reason, verifyURL string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	account.IsInvalid = true
	account.InvalidReason = reason
	account.VerifyURL = verifyURL
	manager.recordFailureLocked(account.Email)
}

func (manager *Manager) MarkFailure(account *Account, model string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	account.ConsecutiveFailure++
	manager.recordFailureLocked(account.Email)
	if account.ConsecutiveFailure >= 3 {
		account.ModelRateLimits[model] = &RateLimit{
			IsRateLimited: true, ResetTimeMS: manager.now().Add(time.Minute).UnixMilli(), ActualResetMS: time.Minute.Milliseconds(),
		}
	}
}

func (manager *Manager) MarkSuccess(account *Account, model string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	account.ConsecutiveFailure = 0
	delete(account.ModelRateLimits, model)
	manager.recordSuccessLocked(account.Email)
}

func (manager *Manager) IncrementFailure(account *Account) int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	account.ConsecutiveFailure++
	return account.ConsecutiveFailure
}

func (manager *Manager) FailureCount(account *Account) int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return account.ConsecutiveFailure
}

func (manager *Manager) Project(account *Account) string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if project := manager.projects[account.Email]; project != "" {
		return project
	}
	if parts := strings.Split(account.RefreshToken, "|"); len(parts) >= 3 && parts[2] != "" {
		return parts[2]
	}
	if account.ProjectID != "" {
		return account.ProjectID
	}
	return account.Subscription.ProjectID
}

func (manager *Manager) CacheProject(account *Account, project string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.projects[account.Email] = project
}

type Snapshot struct {
	Email         string
	Enabled       bool
	Invalid       bool
	InvalidReason string
	VerifyURL     string
	Limits        map[string]RateLimit
}

func (manager *Manager) Snapshot() []Snapshot {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := make([]Snapshot, 0, len(manager.accounts))
	for _, account := range manager.accounts {
		limits := make(map[string]RateLimit, len(account.ModelRateLimits))
		for model, limit := range account.ModelRateLimits {
			if limit != nil {
				limits[model] = *limit
			}
		}
		result = append(result, Snapshot{
			Email: account.Email, Enabled: account.Enabled, Invalid: account.IsInvalid,
			InvalidReason: account.InvalidReason, VerifyURL: account.VerifyURL, Limits: limits,
		})
	}
	return result
}

func (manager *Manager) selectStickyLocked(model string) Selection {
	if manager.currentIndex < 0 || manager.currentIndex >= len(manager.accounts) {
		manager.currentIndex = 0
	}
	current := manager.accounts[manager.currentIndex]
	if manager.usableLocked(current, model) {
		current.LastUsedMS = manager.now().UnixMilli()
		return Selection{Account: current}
	}
	for offset := 1; offset <= len(manager.accounts); offset++ {
		index := (manager.currentIndex + offset) % len(manager.accounts)
		if manager.usableLocked(manager.accounts[index], model) {
			manager.currentIndex = index
			manager.accounts[index].LastUsedMS = manager.now().UnixMilli()
			return Selection{Account: manager.accounts[index]}
		}
	}
	if limit := current.ModelRateLimits[model]; current.Enabled && !current.IsInvalid && limit != nil {
		wait := time.Duration(limit.ResetTimeMS-manager.now().UnixMilli()) * time.Millisecond
		if wait > 0 && wait <= maxStickyWait {
			return Selection{Wait: wait}
		}
	}
	return Selection{}
}

func (manager *Manager) selectRoundRobinLocked(model string) Selection {
	if manager.cursor >= len(manager.accounts) {
		manager.cursor = 0
	}
	start := (manager.cursor + 1) % len(manager.accounts)
	for offset := range len(manager.accounts) {
		index := (start + offset) % len(manager.accounts)
		account := manager.accounts[index]
		if manager.usableLocked(account, model) {
			manager.cursor = index
			manager.currentIndex = index
			account.LastUsedMS = manager.now().UnixMilli()
			return Selection{Account: account}
		}
	}
	return Selection{}
}

func (manager *Manager) selectHybridLocked(model string) Selection {
	type candidate struct {
		account *Account
		index   int
		score   float64
	}
	candidates := make([]candidate, 0)
	for index, account := range manager.accounts {
		if !manager.usableLocked(account, model) || manager.healthScoreLocked(account.Email) < 50 || manager.tokensLocked(account.Email) < 1 {
			continue
		}
		if manager.quotaCriticalLocked(account, model) {
			continue
		}
		candidates = append(candidates, candidate{account: account, index: index, score: manager.scoreLocked(account, model)})
	}
	if len(candidates) == 0 {
		for index, account := range manager.accounts {
			if manager.usableLocked(account, model) {
				candidates = append(candidates, candidate{account: account, index: index, score: manager.scoreLocked(account, model)})
			}
		}
	}
	if len(candidates) == 0 {
		return Selection{}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	best := candidates[0]
	manager.consumeTokenLocked(best.account.Email)
	best.account.LastUsedMS = manager.now().UnixMilli()
	manager.currentIndex = best.index
	return Selection{Account: best.account}
}

func (manager *Manager) usableLocked(account *Account, model string) bool {
	if account == nil || !account.Enabled || account.IsInvalid {
		return false
	}
	now := manager.now().UnixMilli()
	if account.CoolingDownUntilMS > now {
		return false
	}
	if limit := account.ModelRateLimits[model]; limit != nil && limit.IsRateLimited && limit.ResetTimeMS > now {
		return false
	}
	return true
}

func (manager *Manager) clearExpiredLocked() {
	now := manager.now().UnixMilli()
	for _, account := range manager.accounts {
		if account.CoolingDownUntilMS <= now {
			account.CoolingDownUntilMS = 0
			account.CooldownReason = ""
		}
		for model, limit := range account.ModelRateLimits {
			if limit == nil || limit.ResetTimeMS <= now {
				delete(account.ModelRateLimits, model)
			}
		}
	}
}

func (manager *Manager) healthScoreLocked(email string) float64 {
	record, exists := manager.health[email]
	if !exists {
		return 70
	}
	hours := manager.now().Sub(record.LastUpdated).Hours()
	return min(100, record.Score+hours*10)
}

func (manager *Manager) recordSuccessLocked(email string) {
	manager.health[email] = healthRecord{Score: min(100, manager.healthScoreLocked(email)+1), LastUpdated: manager.now()}
}

func (manager *Manager) recordRateLimitLocked(email string) {
	manager.health[email] = healthRecord{Score: max(0, manager.healthScoreLocked(email)-10), LastUpdated: manager.now()}
}

func (manager *Manager) recordFailureLocked(email string) {
	manager.health[email] = healthRecord{Score: max(0, manager.healthScoreLocked(email)-20), LastUpdated: manager.now()}
}

func (manager *Manager) tokensLocked(email string) float64 {
	bucket, exists := manager.buckets[email]
	if !exists {
		return 50
	}
	return min(50, bucket.Tokens+manager.now().Sub(bucket.LastUpdated).Minutes()*6)
}

func (manager *Manager) consumeTokenLocked(email string) {
	manager.buckets[email] = tokenBucket{Tokens: max(0, manager.tokensLocked(email)-1), LastUpdated: manager.now()}
}

func (manager *Manager) quotaCriticalLocked(account *Account, model string) bool {
	quota, exists := account.Quota.Models[model]
	if !exists || quota.RemainingFraction == nil || !quotaFresh(account.Quota.LastChecked, manager.now()) {
		return false
	}
	threshold := 0.05
	if value, exists := account.ModelThreshold[model]; exists && value > 0 {
		threshold = value
	} else if account.QuotaThreshold != nil && *account.QuotaThreshold > 0 {
		threshold = *account.QuotaThreshold
	}
	return *quota.RemainingFraction <= threshold
}

func (manager *Manager) scoreLocked(account *Account, model string) float64 {
	health := manager.healthScoreLocked(account.Email) * 2
	tokens := manager.tokensLocked(account.Email) / 50 * 100 * 5
	quotaScore := 50.0
	if quota, exists := account.Quota.Models[model]; exists && quota.RemainingFraction != nil {
		quotaScore = *quota.RemainingFraction * 100
		if !quotaFresh(account.Quota.LastChecked, manager.now()) {
			quotaScore *= .9
		}
	}
	lru := min(float64(time.Hour.Milliseconds()), float64(manager.now().UnixMilli()-account.LastUsedMS)) / 1000 * .1
	return health + tokens + quotaScore*3 + lru
}

func normalizeStrategy(strategy string) string {
	switch strings.ToLower(strategy) {
	case StrategySticky:
		return StrategySticky
	case StrategyRoundRobin, "roundrobin":
		return StrategyRoundRobin
	default:
		return DefaultStrategy
	}
}

func parseMilliseconds(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return int64(number)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UnixMilli()
		}
	}
	return 0
}

func quotaFresh(value any, now time.Time) bool {
	switch typed := value.(type) {
	case float64:
		return now.UnixMilli()-int64(typed) < (5 * time.Minute).Milliseconds()
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		return err == nil && now.Sub(parsed) < 5*time.Minute
	default:
		return false
	}
}
