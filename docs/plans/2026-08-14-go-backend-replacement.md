# Architecture and Implementation Plan: Backend Replacement with Go

## 1. Context

The original project (`/Users/gus/Git/antigravity-claude-proxy`) uses Node.js and Express to proxy Anthropic Messages API calls to Google Cloud Code / Vertex AI. It manages Google OAuth accounts, rotates tokens, handles rate limits, and serves a Web UI.
The Go repository (`/Users/gus/Git/antigravity-claude-proxy-go`) provides a compiled, memory-efficient, high-throughput implementation of the same proxy logic.

This plan details how to replace the Node.js backend with the Go implementation while maintaining full compatibility with:
1. Claude Code CLI (`claude`)
2. OpenAI and Anthropic SDKs / IDE extensions
3. The Web UI dashboard and account management interface
4. Existing user configuration files (`~/.config/antigravity-proxy/accounts.json`)

---

## 2. Target Architecture

```
                                  +---------------------------------------+
                                  |            Client Surface             |
                                  |  - Claude Code CLI (POST /v1/messages)|
                                  |  - OpenAI / Aider / Cursor            |
                                  |  - Browser Web UI (Dashboard)         |
                                  +-------------------+-------------------+
                                                      |
                                                      v
+---------------------------------------------------------------------------------------------------------+
|                                    Antigravity Go Proxy Engine                                          |
|                                                                                                         |
|  +--------------------------------+  +--------------------------------+  +---------------------------+  |
|  |       HTTP Routing Layer       |  |     Embedded Web UI (embed)    |  |     Real-Time Logger      |  |
|  |  - /v1/messages (SSE & JSON)   |  |  - Static assets (public/*)    |  |  - Ring buffer in memory  |  |
|  |  - /v1/models & /v1/usage      |  |  - Dashboard & Controls        |  |  - SSE: /api/logs/stream  |  |
|  |  - /health & /account-limits   |  |  - Zero Node runtime needed    |  +---------------------------+  |
|  |  - /api/accounts & /api/config |  +--------------------------------+                                 |
|  +--------------------------------+                                                                     |
|                                                                                                         |
|  +--------------------------------+  +--------------------------------+  +---------------------------+  |
|  |   Format & Stream Converter    |  |     Account Pool Dispatcher    |  |       OAuth Manager       |  |
|  |  - Anthropic <-> Cloud Code    |  |  - Sticky / Round-Robin / Hybrid|  |  - Local callback server  |  |
|  |  - Multi-turn & Tool Calling   |  |  - Automatic 429 backoff       |  |  - Token refresh loop     |  |
|  |  - Thinking & Image support    |  |  - Persistence: accounts.json  |  |  - Headless / Web login   |  |
|  +--------------------------------+  +--------------------------------+  +---------------------------+  |
|                                                      |                                                  |
+------------------------------------------------------|--------------------------------------------------+
                                                       v
                                    +-------------------------------------+
                                    |    Google Cloud Code / Vertex API   |
                                    |  - v1internal:streamGenerateContent |
                                    |  - v1internal:fetchAvailableModels  |
                                    +-------------------------------------+
```

---

## 3. Subsystem Comparison and Gap Analysis

| Feature / Area | Node.js Implementation (`antigravity-claude-proxy`) | Go Implementation (`antigravity-claude-proxy-go`) | Status / Required Work |
| :--- | :--- | :--- | :--- |
| **Messages API** | `POST /v1/messages` (JSON & SSE) | `POST /v1/messages` (`internal/api/server.go`) | Parity achieved |
| **Model Catalog** | Dynamic catalog fetch + static fallback | Dynamic catalog fetch (`internal/modelcatalog`) | Parity achieved |
| **Account Storage** | `~/.config/antigravity-proxy/accounts.json` | Same format & path (`internal/accounts/manager.go`) | Parity achieved |
| **Heartbeat / CLI Hooks** | `POST /`, `POST /api/event_logging/batch` | `POST /` implemented; need explicit batch route | Add `/api/event_logging/batch` route |
| **Web UI Assets** | Express serves `./public/` directory | Need `embed.FS` to serve static assets from binary | Embed `public/` in Go server |
| **Management API** | `/health`, `/account-limits`, `/api/accounts`, `/api/config` | Basic `/health` and `/v1/usage` implemented | Implement complete `/api/*` management router |
| **Live Log Streaming** | `/api/logs/stream` SSE stream | Standard `slog` output | Add in-memory ring buffer + SSE log broadcast handler |
| **OAuth Login** | Local ephemeral server callback | Headless / Token extraction implemented | Add local browser OAuth callback server |
| **CLI & Process Control** | `bin/cli.js` (`start`, `stop`, `status`, `accounts`, `web`) | CLI flags in `cmd/proxy/main.go` | Add CLI subcommands or unified wrapper |

---

## 4. Implementation Steps

### Phase 1: Core API & Feature Parity in Go

1. **Add Web UI Static Asset Embedding (`internal/webui`)**:
   - Copy `public/` assets from Node repository into `internal/webui/public`.
   - Use `//go:embed public/*` to embed HTML, CSS, JavaScript, and Lucide icons directly into the binary.
   - Serve static assets at `/` with index fallback.

2. **Add Real-Time Log Broadcaster (`internal/logger/stream.go`)**:
   - Create thread-safe log ring buffer (storing last 500 log entries).
   - Add custom `slog.Handler` that pushes log records to subscriber channels.
   - Implement `GET /api/logs/stream` endpoint with `text/event-stream` response.

3. **Complete Management Endpoints (`internal/api/management.go`)**:
   - `GET /health`: Output full account health, available count, rate-limited count, model limits.
   - `GET /account-limits`: Output quota matrix across all accounts and models (support `?format=table` and JSON).
   - `GET /api/accounts`: List accounts with status, tier, project ID, and enabled state.
   - `POST /api/accounts/toggle`: Enable/disable specific account.
   - `DELETE /api/accounts/:email`: Remove account from storage.
   - `POST /api/accounts/refresh`: Force token refresh for all or single account.
   - `GET /api/config` & `POST /api/config`: Read and write proxy configuration options.
   - `POST /api/event_logging/batch`: Return 200 OK to swallow telemetry from Claude Code CLI.

4. **Add Browser-Based OAuth Callback Server (`internal/auth/oauth_server.go`)**:
   - Spin up local HTTP listener on ephemeral port (e.g. `http://localhost:54123/oauth/callback`).
   - Open system default browser with Google OAuth URL.
   - Capture authorization code, exchange for tokens, and persist to `accounts.json`.

---

### Phase 2: CLI Interface & Daemon Management

1. **Enhance Go CLI (`cmd/proxy/main.go`) with Subcommands**:
   - `antigravity-proxy start [--daemon] [--port 8080] [--accounts <path>]`:
     - Foreground mode or background daemon mode (writing PID to `~/.config/antigravity-proxy/server.pid`).
   - `antigravity-proxy stop`:
     - Reads PID file and sends `SIGTERM`.
   - `antigravity-proxy restart`:
     - Stops active PID and spawns new instance.
   - `antigravity-proxy status`:
     - Queries health endpoint and prints account table.
   - `antigravity-proxy accounts (list|add|remove|verify)`:
     - CLI account management.
   - `antigravity-proxy web`:
     - Opens `http://localhost:8080` in default browser.

2. **Cross-Compilation & Distribution Setup**:
   - Add `Makefile` / `goreleaser` config for targets:
     - `darwin/arm64` (Apple Silicon)
     - `darwin/amd64` (Intel macOS)
     - `linux/amd64`
     - `linux/arm64`
     - `windows/amd64`
   - Produce single executable with zero external runtime dependencies.

---

### Phase 3: Replacement Strategy in Main Repository

1. **Option A: Full Native Go Binary (Recommended)**:
   - Direct distribution of compiled `antigravity-proxy` binaries via GitHub Releases / Homebrew / AUR.
   - Eliminates Node.js requirement completely.

2. **Option B: Hybrid npm Distribution Package**:
   - Maintain `package.json` and `bin/cli.js`.
   - `bin/cli.js` checks platform/architecture and invokes the bundled native Go binary (`bin/antigravity-proxy-<platform>-<arch>`).
   - Preserves `npm install -g antigravity-claude-proxy` compatibility for existing users.

---

## 5. Verification and Validation Plan

### A. Automated Unit & Format Tests
Run Go test suite covering all converters and dispatcher logic:
```bash
go test -v -race ./...
```

### B. Compatibility Suite Execution
Verify against existing test suite in `/Users/gus/Git/antigravity-claude-proxy/tests/`:
1. `tests/test-multiturn-thinking-tools.cjs`: Verify multi-turn conversations with tool calls and thinking tokens.
2. `tests/test-multiturn-thinking-tools-streaming.cjs`: Verify SSE streaming event sequences.
3. `tests/test-images.cjs`: Verify base64 inline image payload translation.
4. `tests/test-strategies.cjs`: Verify account rotation across multiple test accounts on simulated 429 response.

### C. End-to-End Client Testing
1. **Claude Code CLI**:
   ```bash
   ANTHROPIC_BASE_URL=http://localhost:8080 claude "Explain how this proxy works"
   ```
   Verify smooth streaming, thinking block rendering, and tool calls.
2. **OpenAI SDK / Cursor**:
   ```bash
   curl http://localhost:8080/v1/models
   curl http://localhost:8080/v1/messages -H "Content-Type: application/json" -d '{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"Hi"}]}'
   ```
3. **Web UI Verification**:
   - Open `http://localhost:8080` in browser.
   - Verify account status table displays all accounts.
   - Verify live log streaming receives real-time events.
   - Test OAuth login flow from UI.

---

## 6. Critical Files to Modify / Create

- `internal/api/server.go`: Add management routes, event logging batch endpoint.
- `internal/api/management.go`: New file implementing `/health`, `/account-limits`, `/api/accounts`, `/api/config`.
- `internal/logger/stream.go`: New file implementing log capture ring buffer and SSE stream broadcaster.
- `internal/webui/embed.go`: New file with `//go:embed public/*` static asset server.
- `internal/auth/oauth_server.go`: Local browser OAuth callback server.
- `cmd/proxy/main.go`: Subcommand parsing (`start`, `stop`, `status`, `accounts`, `web`).
