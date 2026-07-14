package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
	proxyformat "antigravity-go-proxy/internal/format"
)

const (
	DefaultProjectID = "rising-fact-p41fc"
	maxRequestBody   = 50 << 20
)

type Upstream interface {
	LoadCodeAssist(context.Context, string) (cloudcode.Response, error)
	FetchAvailableModels(context.Context, string) (cloudcode.Response, error)
	StreamGenerateContent(context.Context, any, cloudcode.RequestOptions, func(cloudcode.SSEEvent) error) (cloudcode.Response, error)
}

type Options struct {
	APIKey      string
	ProjectID   string
	Credentials func(context.Context) (auth.Credentials, error)
	NewUpstream func(string) Upstream
	Builder     *proxyformat.Builder
	Now         func() time.Time
	Logger      *slog.Logger
}

type Server struct {
	apiKey      string
	projectID   string
	credentials func(context.Context) (auth.Credentials, error)
	newUpstream func(string) Upstream
	builder     *proxyformat.Builder
	now         func() time.Time
	logger      *slog.Logger

	mu                sync.Mutex
	cachedCredentials auth.Credentials
	upstreamToken     string
	upstream          Upstream
	projects          map[string]string
}

func New(options Options) (*Server, error) {
	if options.APIKey == "" {
		return nil, errors.New("local API key is required")
	}
	if options.Credentials == nil {
		return nil, errors.New("credential provider is required")
	}
	if options.NewUpstream == nil {
		return nil, errors.New("Cloud Code client factory is required")
	}
	if options.Builder == nil {
		options.Builder = proxyformat.NewBuilder()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Server{
		apiKey: options.APIKey, projectID: options.ProjectID,
		credentials: options.Credentials, newUpstream: options.NewUpstream,
		builder: options.Builder, now: options.Now, logger: options.Logger,
		projects: make(map[string]string),
	}, nil
}

func (server *Server) Handler() http.Handler {
	return http.HandlerFunc(server.serveHTTP)
}

func (server *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	if path == "/anthropic" {
		path = "/"
	} else if strings.HasPrefix(path, "/anthropic/") {
		path = strings.TrimPrefix(path, "/anthropic")
	}
	setCORS(writer)
	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if path == "/health" && request.Method == http.MethodGet {
		server.health(writer)
		return
	}
	if path == "/" && request.Method == http.MethodPost {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if strings.HasPrefix(path, "/v1/") && !server.authorized(request) {
		writeAPIError(writer, http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
		return
	}
	switch {
	case path == "/v1/models" && request.Method == http.MethodGet:
		server.models(writer, request)
	case path == "/v1/messages" && request.Method == http.MethodPost:
		server.messages(writer, request)
	case path == "/v1/messages/count_tokens" && request.Method == http.MethodPost:
		writeAPIError(writer, http.StatusNotImplemented, "not_implemented", "Token counting is not implemented. Use /v1/messages with max_tokens or configure your client to skip token counting.")
	default:
		writeAPIError(writer, http.StatusNotFound, "not_found_error", fmt.Sprintf("Endpoint %s %s not found", request.Method, request.URL.Path))
	}
}

func (server *Server) authorized(request *http.Request) bool {
	provided := request.Header.Get("x-api-key")
	if provided == "" {
		if authorization := request.Header.Get("Authorization"); strings.HasPrefix(authorization, "Bearer ") {
			provided = strings.TrimPrefix(authorization, "Bearer ")
		}
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(server.apiKey)) == 1
}

func (server *Server) health(writer http.ResponseWriter) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "ok", "timestamp": server.now().UTC().Format(time.RFC3339Nano),
	})
}

func (server *Server) models(writer http.ResponseWriter, request *http.Request) {
	credentials, upstream, err := server.client(request.Context())
	if err != nil {
		server.writeError(writer, err)
		return
	}
	response, err := upstream.FetchAvailableModels(request.Context(), "")
	if err != nil {
		server.writeError(writer, err)
		return
	}
	var document struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(response.Body, &document); err != nil {
		server.writeError(writer, fmt.Errorf("decode Cloud Code models for %s: %w", credentials.Email, err))
		return
	}
	modelIDs := make([]string, 0, len(document.Models))
	for id := range document.Models {
		modelIDs = append(modelIDs, id)
	}
	sort.Strings(modelIDs)
	models := make([]any, 0, len(document.Models))
	for _, id := range modelIDs {
		details := document.Models[id]
		family := proxyformat.GetModelFamily(id)
		if family != proxyformat.FamilyClaude && family != proxyformat.FamilyGemini {
			continue
		}
		description := details.DisplayName
		if description == "" {
			description = id
		}
		models = append(models, map[string]any{
			"id": id, "object": "model", "created": server.now().Unix(),
			"owned_by": "anthropic", "description": description,
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"object": "list", "data": models})
}

func (server *Server) messages(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBody)
	decoder := json.NewDecoder(request.Body)
	var anthropicRequest map[string]any
	if err := decoder.Decode(&anthropicRequest); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_error", "Invalid JSON request body: "+err.Error())
		return
	}
	messages, ok := anthropicRequest["messages"].([]any)
	if !ok {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_error", "messages is required and must be an array")
		return
	}
	if model, _ := anthropicRequest["model"].(string); model == "" {
		anthropicRequest["model"] = "claude-3-5-sonnet-20241022"
	}
	if _, exists := anthropicRequest["max_tokens"]; !exists {
		anthropicRequest["max_tokens"] = 4096
	}
	if len(messages) == 1 && messages[0] != nil {
		if message, ok := messages[0].(map[string]any); ok && message["content"] == "count" {
			writeJSON(writer, http.StatusOK, map[string]any{})
			return
		}
	}

	credentials, upstream, err := server.client(request.Context())
	if err != nil {
		server.writeError(writer, err)
		return
	}
	projectID, err := server.resolveProject(request.Context(), credentials, upstream)
	if err != nil {
		server.writeError(writer, err)
		return
	}
	payload := server.builder.BuildCloudCodeRequest(anthropicRequest, projectID, credentials.Email)
	innerRequest, _ := payload["request"].(map[string]any)
	options := cloudcode.RequestOptions{SessionID: stringFrom(innerRequest["sessionId"])}
	model := stringFrom(anthropicRequest["model"])
	if proxyformat.GetModelFamily(model) == proxyformat.FamilyClaude && proxyformat.IsThinkingModel(model) {
		options.Headers = make(http.Header)
		options.Headers.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
	}

	if stream, _ := anthropicRequest["stream"].(bool); stream {
		server.streamMessage(writer, request, upstream, payload, options, model)
		return
	}
	server.unaryMessage(writer, request, upstream, payload, options, model)
}

func (server *Server) unaryMessage(writer http.ResponseWriter, request *http.Request, upstream Upstream, payload map[string]any, options cloudcode.RequestOptions, model string) {
	accumulator := proxyformat.NewThinkingAccumulator()
	_, err := upstream.StreamGenerateContent(request.Context(), payload, options, func(event cloudcode.SSEEvent) error {
		return accumulator.Consume(event.Data)
	})
	if err != nil {
		server.writeError(writer, err)
		return
	}
	response := accumulator.Response(model, server.builder.Cache, "")
	writeJSON(writer, http.StatusOK, response)
}

func (server *Server) streamMessage(writer http.ResponseWriter, request *http.Request, upstream Upstream, payload map[string]any, options cloudcode.RequestOptions, model string) {
	converter := proxyformat.NewStreamConverter(model, server.builder.Cache, "")
	started := false
	writeEvents := func(events []map[string]any) error {
		if len(events) == 0 {
			return nil
		}
		if !started {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.Header().Set("Cache-Control", "no-cache")
			writer.Header().Set("Connection", "keep-alive")
			writer.Header().Set("X-Accel-Buffering", "no")
			writer.WriteHeader(http.StatusOK)
			started = true
		}
		for _, event := range events {
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event["type"], encoded); err != nil {
				return err
			}
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return nil
	}
	_, err := upstream.StreamGenerateContent(request.Context(), payload, options, func(event cloudcode.SSEEvent) error {
		events, err := converter.Consume(event.Data)
		if err != nil {
			return err
		}
		return writeEvents(events)
	})
	if err == nil {
		var events []map[string]any
		events, err = converter.Finish()
		if err == nil {
			err = writeEvents(events)
		}
	}
	if err == nil {
		return
	}
	if !started {
		server.writeError(writer, err)
		return
	}
	errorEvent := map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": err.Error()}}
	_ = writeEvents([]map[string]any{errorEvent})
}

func (server *Server) client(ctx context.Context) (auth.Credentials, Upstream, error) {
	server.mu.Lock()
	credentials := server.cachedCredentials
	if credentials.AccessToken != "" && credentials.Expiry.Sub(server.now()) > time.Minute {
		upstream := server.upstream
		server.mu.Unlock()
		return credentials, upstream, nil
	}
	server.mu.Unlock()

	credentials, err := server.credentials(ctx)
	if err != nil {
		return auth.Credentials{}, nil, err
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.cachedCredentials = credentials
	if server.upstream == nil || server.upstreamToken != credentials.AccessToken {
		if closer, ok := server.upstream.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
		server.upstream = server.newUpstream(credentials.AccessToken)
		server.upstreamToken = credentials.AccessToken
	}
	return credentials, server.upstream, nil
}

func (server *Server) resolveProject(ctx context.Context, credentials auth.Credentials, upstream Upstream) (string, error) {
	if server.projectID != "" {
		return server.projectID, nil
	}
	server.mu.Lock()
	projectID := server.projects[credentials.Email]
	server.mu.Unlock()
	if projectID != "" {
		return projectID, nil
	}
	response, err := upstream.LoadCodeAssist(ctx, "")
	if err != nil {
		return "", fmt.Errorf("discover managed Cloud Code project: %w", err)
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body, &document); err != nil {
		return "", fmt.Errorf("decode loadCodeAssist response: %w", err)
	}
	projectID = stringFrom(document["cloudaicompanionProject"])
	if project := objectFrom(document["cloudaicompanionProject"]); projectID == "" {
		projectID = stringFrom(project["id"])
	}
	if projectID == "" {
		projectID = DefaultProjectID
	}
	server.mu.Lock()
	server.projects[credentials.Email] = projectID
	server.mu.Unlock()
	return projectID, nil
}

func (server *Server) writeError(writer http.ResponseWriter, err error) {
	server.logger.Error("API request failed", "error", err)
	status, kind, message := classifyError(err)
	writeAPIError(writer, status, kind, message)
}

func classifyError(err error) (int, string, string) {
	var upstreamError *cloudcode.HTTPError
	if errors.As(err, &upstreamError) {
		switch upstreamError.StatusCode {
		case http.StatusUnauthorized:
			return http.StatusUnauthorized, "authentication_error", "Authentication failed. Make sure Antigravity has a valid token."
		case http.StatusForbidden:
			return http.StatusForbidden, "permission_error", upstreamError.Error()
		case http.StatusTooManyRequests:
			return http.StatusBadRequest, "invalid_request_error", "RESOURCE_EXHAUSTED: capacity is exhausted for this model. Please wait for quota to reset."
		case http.StatusBadRequest, http.StatusNotFound:
			return http.StatusBadRequest, "invalid_request_error", upstreamError.Error()
		default:
			return http.StatusServiceUnavailable, "api_error", upstreamError.Error()
		}
	}
	if errors.Is(err, proxyformat.ErrEmptyResponse) {
		return http.StatusBadGateway, "api_error", err.Error()
	}
	return http.StatusInternalServerError, "api_error", err.Error()
}

func writeAPIError(writer http.ResponseWriter, status int, kind, message string) {
	writeJSON(writer, status, map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func setCORS(writer http.ResponseWriter) {
	writer.Header().Set("Access-Control-Allow-Origin", "*")
	writer.Header().Set("Access-Control-Allow-Headers", "authorization, content-type, x-api-key, anthropic-version, anthropic-beta")
	writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

func stringFrom(value any) string {
	text, _ := value.(string)
	return text
}

func objectFrom(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}
