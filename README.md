# Antigravity Claude Proxy (Go)

`antigravity-claude-proxy-go` exposes a local Anthropic Messages API for **Claude Code**, **Hermes Agent**, and other Anthropic-compatible clients. Upstream, it mirrors the official `agy` CLI: native Go HTTPS transport, Cloud Code REST/SSE endpoints (`v1internal:streamGenerateContent`), exact client identity headers, and identical TLS ClientHello fingerprints.

The proxy listens by default on `127.0.0.1:8080` (configurable) and includes an embedded flat dark **Web UI dashboard**, multi-account rotation with Google OAuth 2.0 support, real-time log streaming, and server-side model mapping.

---

## Key Features

- **Exact `agy` Transport & Fingerprint Matching**: Matches the JA4 fingerprint, cipher suite order, ALPN, and header behavior of the official `agy` CLI.
- **Embedded Web UI Dashboard**: Manage accounts, monitor rate limits, inspect request volume history, stream live logs via SSE, and configure server/Claude CLI presets.
- **Multi-Account Pool & Rotation**:
  - Auto-discovers local `agy` login credentials at `~/.gemini/antigravity-cli/antigravity-oauth-token`.
  - Built-in Google OAuth 2.0 PKCE sign-in flow for adding multiple accounts (`antigravity-proxy accounts add` or Web UI).
  - Selection strategies: `hybrid` (health/token-bucket/least-recent-used scoring), `sticky`, and `round-robin`.
  - Tracks subscription tiers (`PRO`, `FREE`, etc.), per-account rate limits, and model cooldowns in memory.
- **Model Catalog & Server-Side Mapping**:
  - Dynamic catalog supporting Gemini 3.7 Flash (High, Medium, Low), Gemini 3.5 Flash, Gemini 3.1 Pro, Claude Sonnet 4.6 (Thinking), Claude Opus 4.6 (Thinking), and GPT-OSS 120B.
  - Server-side model mapping (`/api/models/config`) to route request model IDs to specific upstream models.
- **CLI & Daemon Management**: Integrated CLI commands (`start`, `stop`, `restart`, `status`, `web`, `accounts`) with background daemon support.
- **Client Integrations**: Seamless integration with **Claude Code** and **Hermes Agent** with custom quota usage reporting.

---

## What “Matching agy” Means

Fresh packet captures from `agy 1.1.2` were taken with both Gemini and Claude models. Both used:

- `POST https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse`
- SNI `daily-cloudcode-pa.googleapis.com`
- No ALPN extension
- JA4 `t13d131100_f57a46bbacb6_f50d94e863eb`
- Go-style `gl-go/...` and Antigravity client identity headers

The Go proxy matches the complete JA4, SNI, ALPN state, cipher list, and signature algorithms. Evidence is checked in at:

- [agy Gemini baseline](.reference/agy-current-baseline.txt)
- [agy Claude baseline](.reference/agy-claude-current-baseline.txt)
- [Go proxy fingerprint gate](.reference/go-current-baseline.txt)
- [live agy/proxy recheck](.reference/fingerprint-recheck-20260715.txt)
- [current model catalog](.reference/agy-current-models.txt)

TLS transport intentionally uses Go's standard library with an empty `tls.Config{}`. Never set custom cipher suites, curves, ALPN, or TLS versions, as doing so alters the fingerprint.

---

## Requirements

- **Go**: `1.24+` (or `go1.27rc2`)
- **OS**: Linux or macOS (systemd service support on Linux)
- **Upstream Auth**: A logged-in `agy` CLI or Google OAuth account credentials
- **Tools**: `curl` for API testing; `tcpdump` / `tshark` for packet verification

---

## Build and Install

### Quick Install Script
```sh
make install
# Installs binary to ~/.local/bin/antigravity-proxy
```

### Manual Build
```sh
make build
# Binary created at ./bin/antigravity-proxy
```

### Run Tests & Verification
```sh
make test
go vet ./...
```

### Profile-Guided Optimization (PGO)
For maximum throughput:
1. Run proxy with `-pprof`.
2. Generate benchmark load.
3. Capture profile: `curl -o default.pgo http://localhost:6060/debug/pprof/profile?seconds=60`
4. Rebuild with PGO: `go build -pgo=default.pgo -ldflags="-s -w" -trimpath -o bin/antigravity-proxy ./cmd/proxy`

---

## Quick Start

1. Start the proxy server:
```sh
export ANTIGRAVITY_PROXY_API_KEY='choose-a-local-secret'
./bin/antigravity-proxy
```

2. Open the Web UI:
```sh
./bin/antigravity-proxy web
# Opens http://127.0.0.1:8080 in default browser
```

3. Test health check:
```sh
curl -sS http://127.0.0.1:8080/health | jq
```

---

## CLI Command Reference

The `antigravity-proxy` CLI provides built-in daemon and account management commands:

| Command | Description |
|---|---|
| `antigravity-proxy` / `antigravity-proxy start` | Start the proxy server in foreground |
| `antigravity-proxy start --daemon` | Run the proxy in background daemon mode |
| `antigravity-proxy stop` | Stop the running daemon process |
| `antigravity-proxy restart` | Stop and restart the proxy daemon |
| `antigravity-proxy status` | Show process status (PID, listen address, account pool status) |
| `antigravity-proxy web` | Launch Web UI dashboard in default browser |
| `antigravity-proxy accounts list` | Display configured accounts, sources, status, and subscription tiers |
| `antigravity-proxy accounts add` | Start interactive Google OAuth flow in browser to add a new account |
| `antigravity-proxy accounts remove <email>` | Remove an account from pool |
| `antigravity-proxy accounts verify` | Test access token validity across all accounts |

---

## Configuration & Environment Variables

Proxy settings can be configured via flags, environment variables, or `~/.config/antigravity-proxy/config.json`.

### Flags and Environment Variables

| Flag | Environment Variable | Default | Purpose |
|---|---|---|---|
| `-listen` | `ANTIGRAVITY_PROXY_LISTEN` | `127.0.0.1:8080` | Local HTTP listen address |
| `-port` | - | `0` (disabled) | Override port in listen address |
| `-api-key` | `ANTIGRAVITY_PROXY_API_KEY` | `""` (none) | Required local proxy API key |
| `-accounts` | `ANTIGRAVITY_ACCOUNTS_FILE` | auto | Account-pool JSON file path |
| `-strategy` | `ACCOUNT_STRATEGY` | `hybrid` | Account strategy (`hybrid`, `sticky`, `round-robin`) |
| `-project` | `AGY_PROJECT_ID` | auto-detected | Global Cloud Code project override |
| `-upstream-timeout` | - | `5m` | Upstream Cloud Code request timeout |
| `-pprof` | - | `false` | Enable pprof server on `localhost:6060` |
| `-daemon` | - | `false` | Run proxy process in background |

Additional environment controls:
- `AGY_TOKEN_PATH`: Path to default `agy` token file.
- `AGY_BINARY_PATH`: Path to `agy` binary for OAuth secret extraction.
- `AGY_TOKEN_WRITEBACK=1`: Enable writing refreshed OAuth tokens back to disk.
- `DEBUG=true` / `ANTIGRAVITY_DEV_MODE=true`: Enable debug level logging.

---

## Multi-Account Management & Rotation

### Automatic Discovery
With no configuration file, the proxy automatically loads the active `agy` login from `~/.gemini/antigravity-cli/antigravity-oauth-token`.

### Pool Configuration (`accounts.json`)
To manage multiple accounts, create `~/.config/antigravity-proxy/accounts.json` (or add accounts via `antigravity-proxy accounts add` / Web UI):

```json
{
  "activeIndex": 0,
  "settings": {},
  "accounts": [
    {
      "email": "personal@example.com",
      "source": "agy",
      "agyTokenPath": "/home/me/.gemini/antigravity-cli/antigravity-oauth-token"
    },
    {
      "email": "work@example.com",
      "source": "oauth",
      "refreshToken": "1//04...",
      "projectId": "work-cloudcode-project"
    }
  ]
}
```

### Rotation Strategies (`ACCOUNT_STRATEGY`)
- `hybrid` *(Default)*: Scores available accounts using health score, token bucket rate, quota remaining, and least-recently-used timestamp. Skips invalid or cooling-down accounts.
- `sticky`: Uses current active account until it encounters a cooldown or rate limit, then rotates to another account.
- `round-robin`: Cycles sequentially through usable accounts for every request.

On `429` rate limits, cooldowns are scoped to the specific model and account so available models on that account remain usable. Stream responses that have already begun emitting data are never replayed to prevent output duplication.

---

## Selectable Models & Server-Side Mapping

`GET /v1/models` returns models available in Cloud Code's catalog.

| Selection ID | Display Name | Context Window | Max Output |
|---|---|---:|---:|
| `gemini-3.7-flash-high` | Gemini 3.7 Flash (High) | 1,048,576 | 65,536 |
| `gemini-3.7-flash-medium` | Gemini 3.7 Flash (Medium) | 1,048,576 | 65,536 |
| `gemini-3.7-flash-low` | Gemini 3.7 Flash (Low) | 1,048,576 | 65,536 |
| `gemini-3.5-flash-low` | Gemini 3.5 Flash (Medium) | 1,048,576 | 65,536 |
| `gemini-3-flash-agent` | Gemini 3.5 Flash (High) | 1,048,576 | 65,536 |
| `gemini-3.5-flash-extra-low` | Gemini 3.5 Flash (Low) | 1,048,576 | 65,536 |
| `gemini-3.1-pro-low` | Gemini 3.1 Pro (Low) | 1,048,576 | 65,535 |
| `gemini-pro-agent` | Gemini 3.1 Pro (High) | 1,048,576 | 65,535 |
| `claude-sonnet-4-6` | Claude Sonnet 4.6 (Thinking) | 250,000 | 64,000 |
| `claude-opus-4-6-thinking` | Claude Opus 4.6 (Thinking) | 250,000 | 64,000 |
| `gpt-oss-120b-medium` | GPT-OSS 120B (Medium) | 131,072 | 32,768 |

### Server-Side Model Mapping
You can map incoming model requested names (e.g. `claude-3-5-sonnet-20241022`) to internal models (e.g. `claude-sonnet-4-6`) in the Web UI under Model Mapping or in `config.json`:

```json
{
  "modelMapping": {
    "claude-3-5-sonnet-20241022": "claude-sonnet-4-6",
    "claude-3-opus-20240229": "claude-opus-4-6-thinking"
  }
}
```

---

## Client Integrations

### Claude Code Integration
Configure environment variables to route Claude Code through local proxy:

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080/anthropic
export ANTHROPIC_API_KEY='your-local-secret'
export ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4-6
export ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-6-thinking

claude --bare -p --model sonnet 'Reply with OK'
```

Alternatively, use the Web UI to switch Claude CLI mode with one click.

### Hermes Agent Integration
Add custom provider to `~/.hermes/config.yaml`:

```yaml
custom_providers:
  - name: antigravity-proxy
    provider: anthropic
    api_mode: anthropic_messages
    base_url: http://127.0.0.1:8080/anthropic
    api_key: your-local-secret
    models:
      gemini-3.7-flash-high:
        context_length: 1048576
      gemini-3.5-flash-low:
        context_length: 1048576
      claude-sonnet-4-6:
        context_length: 250000
      claude-opus-4-6-thinking:
        context_length: 250000
```

---

## HTTP API & Management Routes

All `/v1/*` routes accept authentication via `x-api-key` or `Authorization: Bearer <key>`. Routes are mirrored under `/anthropic` prefix.

| Endpoint | Method | Purpose |
|---|---|---|
| `/health` | `GET` | Proxy status, account pool health summary (public) |
| `/account-limits` | `GET` | Real-time rate limits and account quota details (optional `?format=table`) |
| `/v1/models` | `GET` | List available models, context lengths, thinking support |
| `/v1/usage` | `GET` | Quota usage per model and grouped quota reset windows |
| `/v1/messages` | `POST` | Anthropic-compliant messages API (supports streaming SSE) |
| `/v1/messages/count_tokens` | `POST` | Token counting endpoint (`501 Not Implemented`) |
| `/api/accounts` | `GET`/`POST` | List account pool, trigger account reload / import / export |
| `/api/accounts/{email}` | `DELETE`/`PATCH` | Remove account or update quota thresholds |
| `/api/accounts/{email}/refresh` | `POST` | Clear token caches and re-verify upstream access |
| `/api/accounts/{email}/toggle` | `POST` | Enable or disable account |
| `/api/config` | `GET`/`POST` | Read or save proxy runtime configuration |
| `/api/config/password` | `POST` | Set or update Web UI protection password |
| `/api/claude/config` | `GET`/`POST` | Manage local `~/.claude/settings.json` environment |
| `/api/claude/mode` | `GET`/`POST` | Toggle Claude CLI mode between `proxy` and `paid` |
| `/api/logs` | `GET` | Retrieve recent formatted log entries |
| `/api/logs/stream` | `GET` | SSE stream for real-time console logs |
| `/api/auth/url` | `GET` | Generate Google OAuth sign-in URL |
| `/api/auth/complete` | `POST` | Complete OAuth callback with authorization code |

---

## Systemd Service Installation (Linux)

Create systemd service and environment file:

```sh
install -m 0644 antigravity-go-proxy.service /etc/systemd/system/
install -m 0600 antigravity-go-proxy.env.example /etc/antigravity-go-proxy.env
editor /etc/antigravity-go-proxy.env
systemctl daemon-reload
systemctl enable --now antigravity-go-proxy.service
```

Check status and logs:
```sh
systemctl status antigravity-go-proxy.service
journalctl -u antigravity-go-proxy.service -f
```

---

## Fingerprint Re-verification Gate

To verify TLS ClientHello matches `agy`:

1. Start packet capture:
```sh
tcpdump -i any -w /tmp/antigravity-go.pcap 'host daily-cloudcode-pa.googleapis.com and tcp port 443'
```

2. Trigger request:
```sh
curl -sS -H "x-api-key: $ANTIGRAVITY_PROXY_API_KEY" http://127.0.0.1:8080/v1/usage >/dev/null
```

3. Verify JA4 with `tshark`:
```sh
tshark -r /tmp/antigravity-go.pcap \
  -Y 'tls.handshake.type==1 && tls.handshake.extensions_server_name contains "cloudcode"' \
  -T fields \
  -e tls.handshake.extensions_server_name \
  -e tls.handshake.extensions_alpn_str \
  -e tls.handshake.ja4
```

Expected row output:
```text
daily-cloudcode-pa.googleapis.com    t13d131100_f57a46bbacb6_f50d94e863eb
```

---

## Troubleshooting

- **`401 Unauthorized`**: Check that local `x-api-key` or Bearer token matches `ANTIGRAVITY_PROXY_API_KEY` or `config.json`.
- **`400 Bad Request`**: Verify model ID is valid in `/v1/models`. Use `gemini-pro-agent` instead of raw `gemini-3.1-pro-high`.
- **`429 Resource Exhausted`**: Handled via automatic account rotation and model cooldowns. Use `/v1/usage` or Web UI to inspect cooldown status.
- **`403 Verification Required`**: Account requires manual verification or re-authentication.
- **JA4 Mismatch**: Ensure proxy is built with standard Go toolchain and no custom TLS configurations are applied.
