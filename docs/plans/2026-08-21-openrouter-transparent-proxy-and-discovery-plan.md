# Implementation Plan: OpenRouter "Anthropic Skin" Transparent Proxying with Model Allowlist & Gateway Discovery

## 1. Context & Objectives

Users running Claude Code CLI or Anthropic SDK clients through `antigravity-claude-proxy-go` need seamless access to OpenRouter's "Anthropic Skin" API endpoint (`https://openrouter.ai/api/v1/messages`) without manual translation overhead or losing Google Cloud Code multi-account quota routing.

This plan adds a dedicated OpenRouter Gateway subsystem:
- **Transparent Reverse Proxying**: Routes allowlisted OpenRouter models and local aliases directly to OpenRouter's Anthropic-compatible messages endpoint via `httputil.ReverseProxy`, preserving SSE streaming and injecting required authentication headers (`Authorization: Bearer <token>`, `x-api-key: <token>`).
- **On-Demand Gateway Discovery**: Fetches and caches OpenRouter's dynamic model catalog (`GET /v1/models`) to allow users to search, inspect context windows, and add models to an allowlist with custom local aliases.
- **Unified Catalog & Discovery (`/v1/models`)**: Exposes allowlisted OpenRouter models and aliases alongside Google Cloud Code models for clients supporting `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`.
- **WebUI Configuration & Discovery Modal**: Full interface in Settings -> Models and Claude CLI tabs for credentials, gateway discovery, allowlist management, and Claude CLI environment presets.

---

## 2. Architecture & Data Flow

```
[Claude Code / Anthropic SDK Client]
                 │
                 ▼  POST /v1/messages { model: "claude-3-7-openrouter", ... }
┌─────────────────────────────────────────────────────────────┐
│ Proxy Router (internal/api/server.go)                       │
│                                                             │
│ 1. Match OpenRouter Allowlist / Alias Map?                  │
│    └─► Rewrite model to upstream ID (anthropic/claude-3.7-...)│
│    └─► Transparent ReverseProxy to OpenRouter               │
│                                                             │
│ 2. Match Custom Endpoints Map?                              │
│    └─► Transparent ReverseProxy to Custom URL               │
│                                                             │
│ 3. Match Google Cloud Code Catalog?                         │
│    └─► Translate to Cloud Code format + stream with quota   │
└─────────────────────────────────────────────────────────────┘
                 │
                 ▼ (OpenRouter Target)
┌─────────────────────────────────────────────────────────────┐
│ OpenRouter Reverse Proxy Handler                            │
│  • Target: <openrouter.baseUrl>/v1/messages                 │
│  • Headers: Authorization: Bearer <key>, x-api-key: <key>   │
│  • Pass-through: anthropic-version, anthropic-beta          │
│  • Stream: Direct SSE / Unary chunk flushing                │
└─────────────────────────────────────────────────────────────┘
```

---

## 3. Detailed Component Plan

### 3.1. Configuration Subsystem (`internal/config/`)

#### Files:
- `internal/config/config.go`
- `internal/config/claude.go`
- `internal/config/config_test.go`

#### Changes:
1. Define OpenRouter configuration types in `internal/config/config.go`:
   ```go
   type OpenRouterModelConfig struct {
       ID          string `json:"id"`
       Alias       string `json:"alias,omitempty"`
       DisplayName string `json:"displayName,omitempty"`
       ContextLen  int    `json:"contextLength,omitempty"`
       Enabled     bool   `json:"enabled"`
   }

   type OpenRouterConfig struct {
       Enabled   bool                    `json:"enabled"`
       APIKey    string                  `json:"apiKey,omitempty"`
       BaseURL   string                  `json:"baseUrl,omitempty"`
       Allowlist []OpenRouterModelConfig `json:"allowlist,omitempty"`
   }
   ```
2. Add `OpenRouter OpenRouterConfig` to the main `Config` struct (`json:"openrouter,omitempty"`).
3. Update `DefaultConfig()`:
   - `BaseURL`: `"https://openrouter.ai/api"`
   - `Enabled`: `false`
   - `Allowlist`: empty slice
4. Update `GetPublicConfig()`:
   - Redact `openrouter.apiKey`: replace with `hasApiKey: true` when non-empty.
5. Update `Save()`:
   - Handle `openrouter` updates: preserve existing `apiKey` if `hasApiKey: true` and incoming `apiKey == ""`.
6. Update `internal/config/claude.go`:
   - Support setting `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` and `CLAUDE_CODE_SKIP_FAST_MODE_ORG_CHECK`.
   - Ensure `ANTHROPIC_API_KEY: ""` is set when using OpenRouter auth token to prevent client fallback.

---

### 3.2. OpenRouter Client & Discovery (`internal/openrouter/`)

#### Files:
- `internal/openrouter/client.go` (new)
- `internal/openrouter/client_test.go` (new)

#### Changes:
1. Implement OpenRouter API types:
   ```go
   type ModelItem struct {
       ID            string `json:"id"`
       Name          string `json:"name"`
       Description   string `json:"description"`
       ContextLength int    `json:"context_length"`
   }
   ```
2. Implement `Client`:
   - `FetchAvailableModels(ctx context.Context, apiKey, baseURL string) ([]ModelItem, error)`:
     - Query `GET <baseURL>/v1/models`
     - Auth: `Authorization: Bearer <apiKey>`
     - Timeout: 15 seconds
     - In-memory cache with timestamp.
   - `SaveCache(models []ModelItem)` and `GetCachedModels() []ModelItem`.

---

### 3.3. API Server & Reverse Proxy (`internal/api/`)

#### Files:
- `internal/api/server.go`
- `internal/api/management.go`
- `internal/api/openrouter_proxy_test.go` (new)
- `internal/api/management_test.go`

#### Changes:
1. In `internal/api/server.go`:
   - Extend `messages()` handler:
     - Check if `openrouter.enabled == true` and requested `model` matches any entry in `openrouter.allowlist` (by `item.ID`, `item.Alias`, or direct match when enabled).
     - If matched:
       - Rewrite `anthropicRequest["model"]` to the upstream OpenRouter `item.ID` (e.g. `anthropic/claude-3.7-sonnet`).
       - Marshal modified payload.
       - Invoke `server.forwardToOpenRouter(writer, request, openRouterCfg, reqBody)`.
   - Implement `forwardToOpenRouter(w, req, cfg, reqBody)`:
     - Target: `<cfg.BaseURL>/v1/messages` (defaults to `https://openrouter.ai/api/v1/messages`).
     - Use `httputil.ReverseProxy`.
     - Inject headers:
       - `Authorization: Bearer <cfg.APIKey>`
       - `x-api-key: <cfg.APIKey>`
     - Forward `anthropic-version`, `anthropic-beta`.
     - Stream directly to client with zero translation.
   - Extend `models()` handler:
     - When OpenRouter is enabled, merge all active allowlisted OpenRouter models into the returned list (`/v1/models`).
     - For models with custom aliases, register both the primary ID and the alias so Claude Code and discovery clients recognize both.
2. In `internal/api/management.go`:
   - Add routes:
     - `GET /api/openrouter/config`: Returns public OpenRouter config + active model count.
     - `POST /api/openrouter/config`: Saves OpenRouter config updates.
     - `POST /api/openrouter/models/fetch`: Authenticates against OpenRouter, fetches fresh model list, caches it, and returns it.
     - `GET /api/openrouter/models/cached`: Returns cached OpenRouter model list.

---

### 3.4. WebUI Management & Discovery Modal (`internal/webui/`)

#### Files:
- `internal/webui/public/views/settings.html`
- `internal/webui/public/views/models.html`
- `internal/webui/public/js/components/models.js`
- `internal/webui/public/js/components/claude-config.js`
- `internal/webui/public/js/data-store.js`
- `internal/webui/public/js/components/model-dropdown.js`
- `internal/webui/public/translations/*.json` (add i18n keys)

#### Changes:
1. **Settings -> Models Tab**:
   - Add **OpenRouter Gateway & Anthropic Skin** card:
     - Enabled Toggle.
     - Base URL input (with default `https://openrouter.ai/api`).
     - API Key input (password field with `Key Configured` badge).
     - Action buttons: `[Discover OpenRouter Models]` and `[Save OpenRouter Config]`.
   - Add **OpenRouter Allowlist & Aliases Table**:
     - Columns: `Status` (enabled toggle), `Model ID`, `Display Name`, `Local Alias` (inline editable), `Context Window`, `Actions` (Remove / Edit).
2. **"Discover OpenRouter Models" Modal**:
   - Search filter input by model ID, name, or description.
   - Provider filter buttons: `All`, `Anthropic`, `OpenAI`, `Meta`, `DeepSeek`, `Mistral`, `Google`.
   - Table of discovered models: ID, Display Name, Context Length, Provider badge, Action button `[+ Add to Allowlist]`.
   - Quick alias input when adding a model.
3. **Settings -> Claude CLI Tab**:
   - Checkbox for `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`.
   - Checkbox for `CLAUDE_CODE_SKIP_FAST_MODE_ORG_CHECK`.
   - Update model selector dropdowns to include allowlisted OpenRouter models alongside Google Cloud Code models.
4. **Translations**:
   - Add english, chinese, and other translation strings for OpenRouter UI sections, discovery modal, and toast messages.

---

## 4. Verification & Testing

### 4.1. Automated Tests
1. **Config Tests**:
   - `TestOpenRouterConfigSaveAndRedact`: verify API key redaction in `GetPublicConfig` and preservation on update.
2. **OpenRouter Client Tests**:
   - `TestFetchAvailableModels_Success`: mock server returning OpenRouter `/v1/models` JSON.
   - `TestFetchAvailableModels_AuthError`: verify clean handling of 401/403.
   - `TestCaching`: verify caching avoids redundant upstream calls.
3. **Reverse Proxy & Routing Tests**:
   - `TestOpenRouterForwarding_Unary`: test unary `/v1/messages` request routed with correct headers and body.
   - `TestOpenRouterForwarding_SSE`: test SSE stream chunking through reverse proxy.
   - `TestOpenRouterModelAliasMapping`: verify alias `claude-3-7-openrouter` rewrites to `anthropic/claude-3.7-sonnet`.
   - `TestMergedModelsEndpoint`: verify `/v1/models` includes both Google and OpenRouter allowlisted models.
4. **Management API Tests**:
   - Test `GET/POST /api/openrouter/config` and `POST /api/openrouter/models/fetch`.
5. **Full Suite**:
   - Run `go test -v -race ./...`

### 4.2. Manual & WebUI Verification
1. Start proxy: `go run ./cmd/proxy`.
2. Open WebUI in browser (`http://localhost:8080`).
3. Enter OpenRouter API key in Settings -> Models tab.
4. Click `[Discover OpenRouter Models]`, filter by `anthropic`, and add `anthropic/claude-3.7-sonnet` with alias `claude-3-7-openrouter`.
5. Send request:
   ```bash
   curl -X POST http://localhost:8080/v1/messages \
     -H "Content-Type: application/json" \
     -d '{"model":"claude-3-7-openrouter","messages":[{"role":"user","content":"Hello"}]}'
   ```
6. Verify transparent proxying to OpenRouter and valid streaming response.
7. Verify `/v1/models` lists the allowlisted model and alias.
