# Antigravity Go Proxy

`antigravity-go-proxy` exposes a local Anthropic Messages API for Hermes Agent,
Claude Code, and other Anthropic-compatible clients. Upstream, it behaves like
the currently installed official `agy` CLI: native Go HTTPS, the same Cloud
Code REST/SSE endpoints, the same client identity headers, and the same TLS
ClientHello.

The proxy listens on `127.0.0.1:8091`. A normal `agy` login is sufficient to
start it; an optional account-pool JSON file enables multiple logins.

## What “matching agy” means

Fresh packet captures from `agy 1.1.2` were taken with both Gemini and Claude
models. Both used:

- `POST https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse`
- SNI `daily-cloudcode-pa.googleapis.com`
- no ALPN extension
- JA4 `t13d131100_f57a46bbacb6_f50d94e863eb`
- Go-style `gl-go/...` and Antigravity client identity headers

The Go proxy was captured separately and matched the complete JA4, SNI, ALPN
state, cipher list, and signature algorithms. Evidence is checked in at:

- [agy Gemini baseline](.reference/agy-current-baseline.txt)
- [agy Claude baseline](.reference/agy-claude-current-baseline.txt)
- [Go proxy fingerprint gate](.reference/go-current-baseline.txt)
- [live agy/proxy recheck](.reference/fingerprint-recheck-20260715.txt)
- [current model catalog](.reference/agy-current-models.txt)

Current `agy` does **not** use gRPC for Cloud Code generation. The older
`t13d1312h2...` capture belongs to a `www.googleapis.com` connection, not a
Cloud Code connection.

TLS is intentionally boring: the upstream transport is the Go standard
library with an empty `tls.Config{}`. The code never sets cipher suites, curves,
ALPN, TLS versions, or a custom ClientHello. Do not add any of those settings;
doing so changes the fingerprint.

## Requirements

- Linux with systemd for the service instructions below
- public Go `1.27rc2`, matching the current-agy signature-algorithm set
- a logged-in `agy` CLI
- `curl` for the examples; `tcpdump` and `tshark` for packet verification

By default the proxy uses the active `agy` token at
`~/.gemini/antigravity-cli/antigravity-oauth-token` (or `AGY_TOKEN_PATH`). It
reads the token under a file lock. If the access token expires, it obtains the
OAuth client ID and secret directly from the installed `agy` executable, so
they are never copied into this repository or `/etc/antigravity-go-proxy.env`.
The refresh result remains in memory unless `AGY_TOKEN_WRITEBACK=1` is set.

## Build and test

```sh
cd /root/antigravity-go-proxy
GOTOOLCHAIN=go1.27rc2 go build -ldflags="-s -w" -trimpath -o bin/proxy ./cmd/proxy
GOTOOLCHAIN=go1.27rc2 go test ./...
GOTOOLCHAIN=go1.27rc2 go test -race ./...
GOTOOLCHAIN=go1.27rc2 go vet ./...
```

For maximum performance, you can use Profile-Guided Optimization (PGO):
1. Run the proxy with `--pprof` to enable the `localhost:6060` profiler.
2. Generate load against the proxy.
3. Capture a profile: `curl -o default.pgo http://localhost:6060/debug/pprof/profile?seconds=60`
4. Rebuild with PGO: `GOTOOLCHAIN=go1.27rc2 go build -pgo=default.pgo -ldflags="-s -w" -trimpath -o bin/proxy ./cmd/proxy`

Run it directly:

```sh
export ANTIGRAVITY_PROXY_API_KEY='choose-a-local-secret'
./bin/proxy
```

Useful flags:

| Flag | Default | Purpose |
|---|---|---|
| `-listen` | `127.0.0.1:8091` | Local HTTP listen address |
| `-api-key` | environment value | Required local API key |
| `-accounts` | auto | Optional account-pool JSON file |
| `-strategy` | `hybrid` | `sticky`, `round-robin`, or `hybrid` selection |
| `-project` | auto-detected | Explicit Cloud Code project override |
| `-upstream-timeout` | `5m` | Per-request Cloud Code timeout |
| `-pprof` | `false` | Enable pprof server on localhost:6060 |

The corresponding environment variables are
`ANTIGRAVITY_PROXY_LISTEN`, `ANTIGRAVITY_PROXY_API_KEY`,
`ANTIGRAVITY_ACCOUNTS_FILE`, `ACCOUNT_STRATEGY`, and `AGY_PROJECT_ID`.
For non-standard installations, `AGY_TOKEN_PATH` and `AGY_BINARY_PATH` select
the login token and executable. OAuth refresh-token write-back is off by
default and only enabled by explicitly setting `AGY_TOKEN_WRITEBACK=1`.

## Accounts, multi-account support, and rotation

Yes—multi-account support is built in. With no configuration file the proxy
creates a single-account pool from the current `agy` login. To use more than
one login, create `~/.config/antigravity-proxy/accounts.json` (or set
`ANTIGRAVITY_ACCOUNTS_FILE` / `-accounts`). The JSON shape is intentionally
stable and the file is read-only to the proxy:

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
      "source": "agy",
      "agyTokenPath": "/secure/work-agy-token",
      "projectId": "the-work-project"
    }
  ]
}
```

Each `agyTokenPath` must be a token file created by a login for that account.
The `email` is a stable pool label; the proxy resolves the actual email from
the token before constructing a request. Per-account runtime state—health,
cooldowns, rate limits, and discovered projects—is deliberately kept in memory
and never written back to the pool file.

`ACCOUNT_STRATEGY` (or `-strategy`) controls selection:

- `hybrid` is the default. It skips invalid, cooling-down, rate-limited, low-health, and critically depleted accounts, then scores the remaining accounts by health, request pacing, live quota, and least-recent use.
- `sticky` stays on the active account whenever possible. It only waits for a short current-account cooldown (up to two minutes); otherwise it selects another usable account.
- `round-robin` moves to the next usable account for each selection.

On a `429`, the cooldown is attached to the affected model and account, so an
exhausted Claude route does not suppress an available Gemini route. Capacity
errors receive bounded retries first; longer rate-limit waits rotate to another
usable account. A stream that has already emitted data is never replayed on a
different account, avoiding duplicated partial responses. Authentication
revocation and verification-required responses mark only that account unusable
until it is fixed.

## Cloud Code project override

A Cloud Code project is the project identifier the upstream API expects in the
request's `project` field. It is not a general GCP-project selector and it does
not grant access to a project the selected account cannot use.

For a generation request, project selection is ordered as follows:

1. `-project` or `AGY_PROJECT_ID`, a global override for every account.
2. The selected account's `projectId` in the account-pool file.
3. The project returned by that account's `loadCodeAssist` call, cached for the process lifetime.

Usually leave the override empty: the detected project is the one provisioned
for the logged-in account. Set an override only when you know that every routed
account is authorized for that exact Cloud Code project. If the upstream
response does not identify a project, the proxy fails clearly rather than
silently substituting an unrelated project.

## Install as a service

Create the root-only environment file and install the Go service:

```sh
install -m 0644 antigravity-go-proxy.service /etc/systemd/system/
install -m 0600 antigravity-go-proxy.env.example /etc/antigravity-go-proxy.env
editor /etc/antigravity-go-proxy.env
systemctl daemon-reload
systemctl enable --now antigravity-go-proxy.service
```

Check the Go proxy:

```sh
systemctl status antigravity-go-proxy.service
ss -ltnp '( sport = :8091 )'
journalctl -u antigravity-go-proxy.service -f
```

## HTTP API usage

All `/v1/*` endpoints require either `x-api-key` or a bearer token containing
the local proxy secret. Every route is also mirrored below `/anthropic`.

Set these once for the examples:

```sh
export AGY_PROXY_URL=http://127.0.0.1:8091/anthropic
export AGY_PROXY_KEY='your-local-secret'
```

Health does not require authentication:

```sh
curl -sS "$AGY_PROXY_URL/health" | jq
```

List exactly the models selectable by `agy models`:

```sh
curl -sS -H "x-api-key: $AGY_PROXY_KEY" \
  "$AGY_PROXY_URL/v1/models" | jq
```

Read live Cloud Code quotas. This calls the same
`v1internal:fetchAvailableModels` endpoint as `agy` and returns both per-model
values and grouped quota windows:

```sh
curl -sS -H "x-api-key: $AGY_PROXY_KEY" \
  "$AGY_PROXY_URL/v1/usage" | jq
```

Send a non-streaming message:

```sh
curl -sS "$AGY_PROXY_URL/v1/messages" \
  -H "x-api-key: $AGY_PROXY_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gemini-3.5-flash-low",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "Reply with exactly OK"}]
  }' | jq
```

Stream Anthropic SSE events:

```sh
curl -N "$AGY_PROXY_URL/v1/messages" \
  -H "x-api-key: $AGY_PROXY_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'content-type: application/json' \
  -d '{
    "model": "claude-sonnet-4-6",
    "stream": true,
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "Reply with exactly STREAM_OK"}]
  }'
```

Canonical routes are:

- `GET /health`
- `GET /v1/models`
- `GET /v1/usage`
- `POST /v1/messages`
- `POST /v1/messages/count_tokens` — currently returns `501`

## Selectable models

`GET /v1/models` follows Cloud Code's `agentModelSorts`, the same list printed
by `agy models`. It deliberately excludes image, tab-completion, deprecated,
and other non-agent routes.

| Selection ID | agy label | Context | Max output |
|---|---|---:|---:|
| `gemini-3.5-flash-low` | Gemini 3.5 Flash (Medium) | 1,048,576 | 65,536 |
| `gemini-3-flash-agent` | Gemini 3.5 Flash (High) | 1,048,576 | 65,536 |
| `gemini-3.5-flash-extra-low` | Gemini 3.5 Flash (Low) | 1,048,576 | 65,536 |
| `gemini-3.1-pro-low` | Gemini 3.1 Pro (Low) | 1,048,576 | 65,535 |
| `gemini-pro-agent` | Gemini 3.1 Pro (High) | 1,048,576 | 65,535 |
| `claude-sonnet-4-6` | Claude Sonnet 4.6 (Thinking) | 250,000 | 64,000 |
| `claude-opus-4-6-thinking` | Claude Opus 4.6 (Thinking) | 250,000 | 64,000 |
| `gpt-oss-120b-medium` | GPT-OSS 120B (Medium) | 131,072 | 32,768 |

The routing IDs are not product names. In particular:

- `gemini-3.5-flash-low` is the current **Medium** tier.
- `gemini-3-flash-agent` is the High tier.
- `gemini-3.5-flash-extra-low` is the user-visible Low tier.
- `gemini-pro-agent` is the agent route for Gemini 3.1 Pro High, not a
  separate subscription or “pro agent” product.

Cloud Code exposes `gemini-3.1-pro-high` in its raw map but rejects it for agent
generation. The proxy accepts that legacy name as an input alias and rewrites
it to `gemini-pro-agent`; it does not advertise the invalid route.

The catalog is refreshed every five minutes. Live thinking budgets, context
windows, and maximum output sizes are applied before sending a request. This
also caps oversized Hermes requests—for example, Claude Opus requests for
128,000 output tokens are reduced to Cloud Code's live 64,000-token maximum.

## Hermes Agent integration

Add the provider to `~/.hermes/config.yaml`. Keep the Gemini context window at
1,048,576 tokens:

```yaml
custom_providers:
  - name: antigravity-proxy
    provider: anthropic
    api_mode: anthropic_messages
    base_url: http://127.0.0.1:8091/anthropic
    api_key: your-local-secret
    models:
      gemini-3.5-flash-low:
        context_length: 1048576
      gemini-3-flash-agent:
        context_length: 1048576
      gemini-3.5-flash-extra-low:
        context_length: 1048576
      gemini-3.1-pro-low:
        context_length: 1048576
      gemini-pro-agent:
        context_length: 1048576
      claude-sonnet-4-6:
        context_length: 250000
      claude-opus-4-6-thinking:
        context_length: 250000
      gpt-oss-120b-medium:
        context_length: 131072
```

Validate the configuration and select the provider interactively:

```sh
hermes config check
hermes model --refresh
```

Or force it for a single request:

```sh
hermes chat -q 'Reply with exactly HERMES_OK' \
  --provider custom:antigravity-proxy \
  -m gemini-3.5-flash-low
```

Hermes `/usage` uses the proxy's protected `/v1/usage` extension. After a
request through this provider it displays live Cloud Code quota groups, for
example:

```text
📈 Account limits
Provider: antigravity-proxy
Gemini quota: 86% remaining (14% used) • resets in 4h 18m (... MSK)
Anthropic / GPT-OSS quota: 95% remaining (5% used) • resets in 4h 36m (... MSK)
```

Cloud Code supplies per-model quota pools rather than Anthropic's named
“current session” and “current week” windows. Models with identical remaining
fractions and reset timestamps are grouped so `/usage` does not print eight
duplicate lines. The raw per-model data remains available from `/v1/usage`.

Restart the messaging gateway (which owns slash commands) and the desktop
backend after changing Hermes code or provider configuration:

```sh
systemctl restart hermes-gateway.service hermes-serve.service
systemctl status hermes-gateway.service hermes-serve.service --no-pager
```

## Claude Code integration

Use an isolated shell or settings directory so normal Claude configuration is
not overwritten:

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:8091/anthropic
export ANTHROPIC_API_KEY='your-local-secret'
export ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4-6
export ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-6-thinking

claude --bare -p --model sonnet 'Reply with exactly SONNET_OK'
claude --bare -p --model opus 'Reply with exactly OPUS_OK'
```

## Re-run the fingerprint gate

Build with the fingerprinted toolchain, start a capture, then trigger any
upstream call such as `/v1/usage`:

```sh
tcpdump -i any -w /tmp/antigravity-go.pcap \
  'host daily-cloudcode-pa.googleapis.com and tcp port 443'
```

In another shell:

```sh
curl -sS -H "x-api-key: $AGY_PROXY_KEY" \
  "$AGY_PROXY_URL/v1/usage" >/dev/null
```

Stop `tcpdump` and inspect every Cloud Code ClientHello:

```sh
tshark -r /tmp/antigravity-go.pcap \
  -Y 'tls.handshake.type==1 && tls.handshake.extensions_server_name contains "cloudcode"' \
  -T fields \
  -e tls.handshake.extensions_server_name \
  -e tls.handshake.extensions_alpn_str \
  -e tls.handshake.ja4
```

The expected row has daily Cloud Code SNI, an empty ALPN field, and exact JA4:

```text
daily-cloudcode-pa.googleapis.com    t13d131100_f57a46bbacb6_f50d94e863eb
```

## Troubleshooting

- `401` from the local proxy means the local `x-api-key`/bearer token is
  missing or does not match `/etc/antigravity-go-proxy.env`.
- Cloud Code `400 INVALID_ARGUMENT` usually means a raw, non-agent model ID or
  an output limit above the live model cap. Refresh `/v1/models`; use
  `gemini-pro-agent`, not raw `gemini-3.1-pro-high`.
- `429 RESOURCE_EXHAUSTED` is handled with the ported per-model cooldown and
  account rotation logic. `/v1/usage` shows the current reset timestamps.
- A Google `403` requiring verification is treated as a permanent account
  intervention state, not retried indefinitely.
- If JA4 changes, first verify `go version`, then check that nobody added TLS
  fields or enabled HTTP/2 on the dedicated Cloud Code transport.
- The production endpoint is only a fallback; daily Cloud Code is always tried
  first, matching current `agy`.

## Safety and behavioral scope

- The account-pool file is read-only. Cooldowns and invalid-account state are
  maintained in Go memory.
- Request conversion, schema sanitization, thinking signatures, SSE response
  conversion, backoff, endpoint failover, quota rotation, and auth failure
  classification are implemented in Go.
- The recovered protobuf sources remain schema evidence; no schema was
  fabricated.
- `TODO(behavioral)`: the agy language-server sidecar and its non-generation
  client events are intentionally deferred. They are not part of the verified
  Cloud Code content path.
