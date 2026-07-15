package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
	proxyformat "antigravity-go-proxy/internal/format"
	"antigravity-go-proxy/internal/modelcatalog"
)

type CloudClient interface {
	LoadCodeAssist(context.Context, string) (cloudcode.Response, error)
	FetchAvailableModels(context.Context, string) (cloudcode.Response, error)
	StreamGenerateContent(context.Context, any, cloudcode.RequestOptions, func(cloudcode.SSEEvent) error) (cloudcode.Response, error)
}

type Resolver interface {
	Resolve(context.Context, *Account) (auth.Credentials, error)
	Invalidate(string)
}

type SleepFunc func(context.Context, time.Duration) error

type DispatcherOptions struct {
	Manager            *Manager
	Resolver           Resolver
	Builder            *proxyformat.Builder
	NewClient          func(string) CloudClient
	ProjectID          string
	MaxRetries         int
	MaxWait            time.Duration
	CapacityBackoffs   []time.Duration
	MaxCapacityRetries int
	SwitchDelay        time.Duration
	Sleep              SleepFunc
	Now                func() time.Time
	ModelCacheTTL      time.Duration
}

type accountClient struct {
	token  string
	client CloudClient
}

type Dispatcher struct {
	manager            *Manager
	resolver           Resolver
	builder            *proxyformat.Builder
	newClient          func(string) CloudClient
	projectID          string
	maxRetries         int
	maxWait            time.Duration
	capacityBackoffs   []time.Duration
	maxCapacityRetries int
	switchDelay        time.Duration
	sleep              SleepFunc
	now                func() time.Time
	modelCacheTTL      time.Duration

	mu        sync.Mutex
	clients   map[string]accountClient
	catalog   *modelcatalog.Catalog
	catalogAt time.Time
}

func NewDispatcher(options DispatcherOptions) (*Dispatcher, error) {
	if options.Manager == nil || options.Resolver == nil || options.NewClient == nil {
		return nil, errors.New("account manager, credential resolver, and Cloud Code client factory are required")
	}
	if options.Builder == nil {
		options.Builder = proxyformat.NewBuilder()
	}
	if options.MaxRetries <= 0 {
		options.MaxRetries = 5
	}
	if options.MaxWait <= 0 {
		options.MaxWait = 2 * time.Minute
	}
	if len(options.CapacityBackoffs) == 0 {
		options.CapacityBackoffs = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, time.Minute}
	}
	if options.MaxCapacityRetries <= 0 {
		options.MaxCapacityRetries = 5
	}
	if options.SwitchDelay == 0 {
		options.SwitchDelay = 5 * time.Second
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ModelCacheTTL <= 0 {
		options.ModelCacheTTL = 5 * time.Minute
	}
	return &Dispatcher{
		manager: options.Manager, resolver: options.Resolver, builder: options.Builder,
		newClient: options.NewClient, projectID: options.ProjectID,
		maxRetries: options.MaxRetries, maxWait: options.MaxWait,
		capacityBackoffs: options.CapacityBackoffs, maxCapacityRetries: options.MaxCapacityRetries,
		switchDelay: options.SwitchDelay, sleep: options.Sleep, now: options.Now,
		modelCacheTTL: options.ModelCacheTTL,
		clients:       make(map[string]accountClient),
	}, nil
}

func (dispatcher *Dispatcher) FetchAvailableModels(ctx context.Context) (cloudcode.Response, error) {
	var lastError error
	for attempt := 0; attempt < max(dispatcher.maxRetries, dispatcher.manager.Count()+1); attempt++ {
		selection := dispatcher.manager.Select("")
		if selection.Account == nil {
			return cloudcode.Response{}, errors.New("no accounts available")
		}
		credentials, err := dispatcher.resolver.Resolve(ctx, selection.Account)
		if err != nil {
			dispatcher.handleCredentialError(selection.Account, err)
			lastError = err
			continue
		}
		response, err := dispatcher.client(selection.Account, credentials.AccessToken).FetchAvailableModels(ctx, dispatcher.project(selection.Account))
		if err == nil {
			dispatcher.manager.MarkSuccess(selection.Account, "")
			dispatcher.cacheCatalog(response.Body)
			return response, nil
		}
		lastError = err
		if !dispatcher.rotateForError(selection.Account, "", err) {
			return cloudcode.Response{}, err
		}
	}
	return cloudcode.Response{}, fmt.Errorf("model listing exhausted account retries: %w", lastError)
}

func (dispatcher *Dispatcher) StreamGenerateContent(ctx context.Context, request map[string]any, consume func(cloudcode.SSEEvent) error) (cloudcode.Response, error) {
	requestedModel, _ := request["model"].(string)
	modelDetails, err := dispatcher.resolveModel(ctx, requestedModel)
	if err != nil {
		return cloudcode.Response{}, err
	}
	request = cloneRequest(request)
	request["model"] = modelDetails.ID
	model := modelDetails.ID
	maxAttempts := max(dispatcher.maxRetries, dispatcher.manager.Count()+1)
	var lastError error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		selection := dispatcher.manager.Select(model)
		if selection.Account == nil {
			if dispatcher.manager.AllInvalid() {
				return cloudcode.Response{}, errors.New("all accounts are invalid and require user intervention")
			}
			wait := selection.Wait
			if wait == 0 {
				wait = dispatcher.manager.MinWait(model)
			}
			if wait > 0 && wait <= dispatcher.maxWait {
				if err := dispatcher.sleep(ctx, wait+500*time.Millisecond); err != nil {
					return cloudcode.Response{}, err
				}
				attempt--
				continue
			}
			if wait > dispatcher.maxWait {
				return cloudcode.Response{}, fmt.Errorf("RESOURCE_EXHAUSTED: rate limited on %s; quota resets after %s", model, wait.Round(time.Second))
			}
			return cloudcode.Response{}, errors.New("no accounts available")
		}
		if selection.Wait > 0 {
			if err := dispatcher.sleep(ctx, selection.Wait); err != nil {
				return cloudcode.Response{}, err
			}
		}
		account := selection.Account
		credentials, err := dispatcher.resolver.Resolve(ctx, account)
		if err != nil {
			dispatcher.handleCredentialError(account, err)
			lastError = err
			continue
		}
		client := dispatcher.client(account, credentials.AccessToken)
		project, err := dispatcher.resolveProject(ctx, account, client)
		if err != nil {
			dispatcher.manager.MarkFailure(account, model)
			lastError = err
			continue
		}
		payload := dispatcher.builder.BuildCloudCodeRequestWithModel(request, project, credentials.Email, proxyformat.ModelOptions{
			SupportsThinking: modelDetails.SupportsThinking, ThinkingBudget: modelDetails.ThinkingBudget,
			MinThinkingBudget: modelDetails.MinThinkingBudget, MaxOutputTokens: modelDetails.MaxOutputTokens,
		})
		inner, _ := payload["request"].(map[string]any)
		options := cloudcode.RequestOptions{SessionID: textValue(inner["sessionId"])}
		if proxyformat.GetModelFamily(model) == proxyformat.FamilyClaude && modelDetails.SupportsThinking {
			options.Headers = make(http.Header)
			options.Headers.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
		}

		capacityAttempt := 0
		for {
			eventCount := 0
			response, requestErr := client.StreamGenerateContent(ctx, payload, options, func(event cloudcode.SSEEvent) error {
				eventCount++
				return consume(event)
			})
			if requestErr == nil {
				dispatcher.manager.MarkSuccess(account, model)
				return response, nil
			}
			lastError = requestErr
			if eventCount > 0 {
				return response, requestErr
			}
			upstreamError := findHTTPError(requestErr)
			if upstreamError != nil && isCapacityHTTPError(upstreamError) && capacityAttempt < dispatcher.maxCapacityRetries {
				wait := ParseResetTime(upstreamError.Header, upstreamError.Body, dispatcher.now())
				if wait == 0 {
					wait = dispatcher.capacityBackoffs[min(capacityAttempt, len(dispatcher.capacityBackoffs)-1)]
				}
				capacityAttempt++
				dispatcher.manager.IncrementFailure(account)
				if err := dispatcher.sleep(ctx, wait); err != nil {
					return cloudcode.Response{}, err
				}
				continue
			}
			if upstreamError != nil && upstreamError.StatusCode == http.StatusTooManyRequests {
				reason := ClassifyError(upstreamError.Body, upstreamError.StatusCode)
				reset := ParseResetTime(upstreamError.Header, upstreamError.Body, dispatcher.now())
				failures := dispatcher.manager.FailureCount(account)
				wait := SmartBackoff(reason, reset, failures)
				if reason == ReasonCapacity && capacityAttempt >= dispatcher.maxCapacityRetries {
					dispatcher.manager.MarkRateLimited(account, model, 15*time.Second)
					break
				}
				if wait <= 10*time.Second && failures == 0 {
					dispatcher.manager.IncrementFailure(account)
					if err := dispatcher.sleep(ctx, max(reset, time.Second)); err != nil {
						return cloudcode.Response{}, err
					}
					continue
				}
				if wait > 10*time.Second && dispatcher.switchDelay > 0 {
					if err := dispatcher.sleep(ctx, dispatcher.switchDelay); err != nil {
						return cloudcode.Response{}, err
					}
				}
				dispatcher.manager.MarkRateLimited(account, model, wait)
				break
			}
			if dispatcher.rotateForError(account, model, requestErr) {
				break
			}
			return cloudcode.Response{}, requestErr
		}
	}
	return cloudcode.Response{}, fmt.Errorf("max retries exceeded: %w", lastError)
}

func (dispatcher *Dispatcher) resolveModel(ctx context.Context, requested string) (modelcatalog.Model, error) {
	dispatcher.mu.Lock()
	catalog := dispatcher.catalog
	fresh := catalog != nil && dispatcher.now().Sub(dispatcher.catalogAt) < dispatcher.modelCacheTTL
	dispatcher.mu.Unlock()
	if !fresh {
		response, err := dispatcher.FetchAvailableModels(ctx)
		if err != nil {
			return modelcatalog.Model{}, fmt.Errorf("refresh selectable models: %w", err)
		}
		catalog, err = modelcatalog.Parse(response.Body)
		if err != nil {
			return modelcatalog.Model{}, err
		}
		dispatcher.storeCatalog(catalog)
	}
	return catalog.Resolve(requested)
}

func (dispatcher *Dispatcher) cacheCatalog(body []byte) {
	catalog, err := modelcatalog.Parse(body)
	if err == nil {
		dispatcher.storeCatalog(catalog)
	}
}

func (dispatcher *Dispatcher) storeCatalog(catalog *modelcatalog.Catalog) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	dispatcher.catalog = catalog
	dispatcher.catalogAt = dispatcher.now()
}

func cloneRequest(request map[string]any) map[string]any {
	cloned := make(map[string]any, len(request))
	for key, value := range request {
		cloned[key] = value
	}
	return cloned
}

func (dispatcher *Dispatcher) resolveProject(ctx context.Context, account *Account, client CloudClient) (string, error) {
	if dispatcher.projectID != "" {
		return dispatcher.projectID, nil
	}
	if project := dispatcher.manager.Project(account); project != "" {
		return project, nil
	}
	response, err := client.LoadCodeAssist(ctx, "")
	if err != nil {
		return "", fmt.Errorf("discover project for %s: %w", account.Email, err)
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body, &document); err != nil {
		return "", err
	}
	project := textValue(document["cloudaicompanionProject"])
	if object, ok := document["cloudaicompanionProject"].(map[string]any); ok && project == "" {
		project = textValue(object["id"])
	}
	if project == "" {
		return "", fmt.Errorf("loadCodeAssist response for %s did not include a Cloud Code project", account.Email)
	}
	dispatcher.manager.CacheProject(account, project)
	return project, nil
}

func (dispatcher *Dispatcher) client(account *Account, token string) CloudClient {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	entry, exists := dispatcher.clients[account.Email]
	if exists && entry.token == token {
		return entry.client
	}
	if exists {
		if closer, ok := entry.client.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
	client := dispatcher.newClient(token)
	dispatcher.clients[account.Email] = accountClient{token: token, client: client}
	return client
}

func (dispatcher *Dispatcher) project(account *Account) string {
	if dispatcher.projectID != "" {
		return dispatcher.projectID
	}
	return dispatcher.manager.Project(account)
}

func (dispatcher *Dispatcher) handleCredentialError(account *Account, err error) {
	if IsPermanentAuthFailure(err.Error()) || containsAny(strings.ToLower(err.Error()), "auth_invalid", "invalid_grant") {
		dispatcher.manager.MarkInvalid(account, "Credentials are invalid - re-authentication required", "")
	} else {
		dispatcher.manager.MarkFailure(account, "")
	}
}

func (dispatcher *Dispatcher) rotateForError(account *Account, model string, err error) bool {
	upstreamError := findHTTPError(err)
	if upstreamError == nil {
		dispatcher.manager.MarkFailure(account, model)
		return true
	}
	body := upstreamError.Body
	switch upstreamError.StatusCode {
	case http.StatusUnauthorized:
		dispatcher.resolver.Invalidate(account.Email)
		if IsPermanentAuthFailure(body) {
			dispatcher.manager.MarkInvalid(account, "Token revoked - re-authentication required", "")
		} else {
			dispatcher.manager.MarkFailure(account, model)
		}
		return true
	case http.StatusForbidden:
		if IsValidationRequired(body) {
			dispatcher.manager.MarkInvalid(account, "Account requires verification", ExtractVerificationURL(body))
			return true
		}
		if IsAccountBanned(body) {
			dispatcher.manager.MarkInvalid(account, "Account banned — Gemini disabled for Terms of Service violation", "")
			return true
		}
		return false
	case http.StatusBadRequest, http.StatusNotFound:
		return false
	case http.StatusTooManyRequests:
		wait := SmartBackoff(ClassifyError(body, upstreamError.StatusCode), ParseResetTime(upstreamError.Header, body, dispatcher.now()), dispatcher.manager.FailureCount(account))
		dispatcher.manager.MarkRateLimited(account, model, wait)
		return true
	default:
		if upstreamError.StatusCode >= 500 {
			dispatcher.manager.MarkFailure(account, model)
			return true
		}
		return false
	}
}

func findHTTPError(err error) *cloudcode.HTTPError {
	var upstreamError *cloudcode.HTTPError
	if errors.As(err, &upstreamError) {
		return upstreamError
	}
	return nil
}

func isCapacityHTTPError(err *cloudcode.HTTPError) bool {
	return (err.StatusCode == http.StatusTooManyRequests || err.StatusCode == http.StatusServiceUnavailable || err.StatusCode == 529) && IsCapacityExhausted(err.Body)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func textValue(value any) string {
	text, _ := value.(string)
	return text
}
