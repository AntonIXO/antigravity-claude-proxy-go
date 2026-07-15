# Antigravity Go Proxy

This is a Go reimplementation of the Antigravity Cloud Code proxy. It exposes
an Anthropic-compatible HTTP API while sending upstream requests with the same
standard-library HTTPS REST/SSE transport and TLS fingerprint as the current
`agy` CLI.

The existing Node proxy remains separate on `127.0.0.1:8090`. This proxy uses
`127.0.0.1:8091` and loads the Node account pool read-only.

## Verified baseline

Fresh captures were taken from `agy 1.1.2` with both its Gemini setting and an
explicit `Claude Sonnet 4.6 (Thinking)` selection. Both model families used:

- SNI `daily-cloudcode-pa.googleapis.com`
- no ALPN extension
- JA4 `t13d131100_f57a46bbacb6_f50d94e863eb`
- `POST /v1internal:streamGenerateContent?alt=sse`

The public Go `1.27rc2` client capture matched that complete JA4 exactly. See
[the Gemini baseline](.reference/agy-current-baseline.txt), [the Claude
baseline](.reference/agy-claude-current-baseline.txt), and [the Go gate](.reference/go-current-baseline.txt).

The older checked-in JA4 prefix `t13d1312h2` belongs to a
`www.googleapis.com` connection, not current Cloud Code traffic. `PLAN.md`
records the evidence and why the implementation was revised from gRPC to the
current observed REST/SSE behavior.

## Safety invariants

- TLS uses an empty `tls.Config{}`. Cipher suites, curves, extensions, and ALPN
  are never customized.
- `~/.config/antigravity-proxy/accounts.json` is only read. Runtime cooldowns
  and invalid-account state remain in Go memory and are never persisted over
  the Node proxy's state.
- OAuth token write-back is disabled unless `AGY_TOKEN_WRITEBACK=1` is set
  explicitly.
- The daily Cloud Code endpoint is tried before production fallback.

## Build and test

```sh
go build -o bin/proxy ./cmd/proxy
go test ./...
go test -race ./...
go vet ./...
```

Run locally:

```sh
ANTIGRAVITY_PROXY_API_KEY=local-secret ./bin/proxy
curl -H 'x-api-key: local-secret' http://127.0.0.1:8091/v1/models
```

Canonical routes are `GET /health`, `GET /v1/models`, `POST /v1/messages`,
and `POST /v1/messages/count_tokens`. They are also available below the
`/anthropic` prefix for Hermes and Claude Code.

Useful flags are `-listen`, `-accounts`, `-strategy`, `-project`, and
`-upstream-timeout`. Selection strategies are `sticky`, `round-robin`, and
`hybrid`.

## Selectable models

`GET /v1/models` follows the same `agentModelSorts` list as `agy models`; it
does not advertise every raw entry returned by Cloud Code. The live list is:

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

The IDs are Cloud Code routing names, not clean product names. In particular,
`gemini-3.5-flash-low` currently means the **Medium** tier. High is routed by
`gemini-3-flash-agent`, and the user-visible Low tier is routed by
`gemini-3.5-flash-extra-low`.

Likewise, `gemini-pro-agent` is the agent-optimized routing ID for Gemini 3.1
Pro High. Cloud Code also publishes `gemini-3.1-pro-high` in its raw model map,
but does not put it in agy's agent list and rejects requests to it. For backward
compatibility the proxy accepts `gemini-3.1-pro-high` and rewrites it to the
valid `gemini-pro-agent` route.

The catalog and per-model limits are refreshed from Cloud Code every five
minutes. The proxy applies the returned thinking budget and output cap before
generation; this is necessary because clients such as Hermes may request more
output than a model accepts. The captured model evidence is in
[the current model baseline](.reference/agy-current-models.txt).

## Systemd and clients

The repository includes [the independent service unit](antigravity-go-proxy.service)
and [an environment template](antigravity-go-proxy.env.example). Install them
without replacing or stopping `antigravity-proxy.service`:

```sh
install -m 0644 antigravity-go-proxy.service /etc/systemd/system/
install -m 0600 antigravity-go-proxy.env.example /etc/antigravity-go-proxy.env
systemctl daemon-reload
systemctl enable --now antigravity-go-proxy.service
```

Hermes provider `custom:antigravity-proxy` should use
`http://127.0.0.1:8091/anthropic`. For an isolated Claude Code run, set
`ANTHROPIC_BASE_URL` to that same URL, set its API key to the local proxy key,
and explicitly map the aliases:

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:8091/anthropic
export ANTHROPIC_API_KEY=local-secret
export ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4-6
export ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus-4-6-thinking
claude --bare -p --model sonnet 'Reply with exactly SONNET_OK'
claude --bare -p --model opus 'Reply with exactly OPUS_OK'
```

## Behavioral scope

Request conversion, schema sanitization, thought-signature handling, response
streaming, account selection, per-model cooldowns, capacity retry, quota
rotation, and permanent account-failure detection are ported from the Node
proxy.

`TODO(behavioral)`: the language-server sidecar and its non-generation client
events are intentionally deferred. They are not part of the primary Cloud Code
content path verified here.
