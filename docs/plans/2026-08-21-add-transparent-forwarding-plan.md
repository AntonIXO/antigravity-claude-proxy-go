# Plan: Forward Requests to Custom Anthropic Endpoints

## Context
Currently, `antigravity-claude-proxy-go` processes all `/v1/messages` requests by translating them to Google Cloud Code formats and routing them to the upstream Google backend. To allow users to maintain a single `settings.json` in their Claude Code CLI while retaining access to models not hosted on Cloud Code (e.g., Opus, custom local endpoints, or official Anthropic endpoints), we need to bypass the Cloud Code translation for specific configured models and forward the requests transparently.

## Proposed Changes

### 1. Configuration Changes (`internal/config/config.go`)
- **Add Struct:** Define `EndpointConfig` to store URL and API Key.
  ```go
  type EndpointConfig struct {
      URL    string `json:"url"`
      APIKey string `json:"apiKey,omitempty"`
  }
  ```
- **Update `Config`:** Add `CustomEndpoints map[string]EndpointConfig` to the main `Config` struct.
- **Update `Save`:** Ensure dictionary merging logic handles `customEndpoints` gracefully (similar to `accountSelection`).
- **Update `GetPublicConfig`:** Redact or omit the `APIKey` fields within `CustomEndpoints` to ensure secrets are not exposed to the Web UI.

### 2. Request Routing (`internal/api/server.go`)
- In `messages(writer http.ResponseWriter, request *http.Request)`, after parsing `anthropicRequest` and applying any `cfg.ModelMapping`:
  - Check if `cfg.CustomEndpoints[model]` exists for the requested model.
  - If a custom endpoint is configured, re-marshal the modified `anthropicRequest` to JSON.
  - Call a new method `forwardToCustomEndpoint(...)` and return early, skipping the normal Google translation and streaming logic.

### 3. Transparent Forwarding (`internal/api/server.go`)
- Implement `forwardToCustomEndpoint`:
  - Use `net/http/httputil.ReverseProxy` to cleanly proxy the request and handle streaming/SSE correctly.
  - **URL resolution:** If the configured `URL` does not end in `/v1/messages`, append it (e.g., `https://api.anthropic.com` -> `https://api.anthropic.com/v1/messages`).
  - **Director Function:** 
    - Set `req.URL` and `req.Host` to the target.
    - Replace `req.Body` with `io.NopCloser(bytes.NewReader(modifiedBody))` and update `req.ContentLength`.
    - If `endpoint.APIKey` is set, inject/overwrite the `x-api-key` header.
    - Preserve other relevant headers (e.g., `anthropic-version`, `anthropic-beta`).
  - Call `proxy.ServeHTTP(writer, request)`.

## Verification
- Add a custom endpoint config to `config.json` mapping a model (e.g. `claude-3-opus-20240229`) to a test URL (`http://localhost:8080/mock`).
- Run a curl request against the proxy and verify the request is forwarded transparently, preserving body modification and headers.
- Verify standard requests (not in `CustomEndpoints`) still route to Google Cloud Code as before.
- Check the `GET /api/config` endpoint (Web UI config route) to ensure API keys inside `CustomEndpoints` are redacted.