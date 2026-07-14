# PLAN — Go/gRPC Antigravity Proxy (fingerprint-identical to `agy`)

## Mission

Reimplement the Node.js `antigravity-claude-proxy` as a **Go** binary that talks to
Google Cloud Code (`cloudcode-pa.googleapis.com`) over **native gRPC**, so its
network fingerprint is **indistinguishable from the official `agy` CLI** (also Go).
The proxy exposes a local **Anthropic Messages API** (`/v1/messages`) that Hermes
Agent consumes as a custom provider. Goal: eliminate the two ToS-ban detection
vectors identified by packet capture — **REST-vs-gRPC** and **Node-vs-Go TLS**.

## Why this eliminates the ban risk (evidence from tcpdump + Wireshark)

| Vector | Node proxy (current) | `agy` (real) | Go proxy (this plan) |
|--------|----------------------|--------------|----------------------|
| Protocol | REST `v1internal:generateContent` (HTTP/1.1 JSON) | gRPC `CloudCode/GenerateContent` (HTTP/2 protobuf) | **gRPC, identical** |
| TLS JA4 | `t13d5212h1` (OpenSSL, 52 ciphers, h1) | `t13d1312h2` (Go crypto/tls, 13 ciphers, h2) | **`t13d1312h2`, identical** |
| `x-goog-api-client` | `gl-node/...` | `gl-go/1.26.4 ...` | **`gl-go/...`, identical** |
| HTTP/2 SETTINGS | undici defaults | grpc-go defaults | **grpc-go defaults, identical** |

**Critical insight (from deep research):** if you build with the *same Go
version* (1.26.4 — already installed) using *standard `crypto/tls`* via `grpc-go`,
and you **do not override** `CipherSuites`, `CurvePreferences`, or `NextProtos`,
the JA3/JA4 hash matches `agy` **automatically**. Do NOT use `utls` — it would
*break* the match. The whole strategy is "be a normal Go gRPC client, like agy is."

## Ground truth already captured for you (in `.reference/`)

- `grpc-methods.txt` — 22 real `CloudCode/*` gRPC method names from the `agy` binary.
- `proto-messages.txt` — 249 real `v1internal.*` protobuf message type names.
- `agy-capture.pcap` — real `agy` TLS ClientHello (JA4 `t13d1312h2`). Your target.
- `proxy-capture.pcap` — old Node proxy ClientHello (JA4 `t13d5212h1`). What to avoid.

Verify your build against these with `tshark -r <cap> -Y 'tls.handshake.type==1' -T fields -e tls.handshake.ja4`.

## Reference implementations to read (do NOT reinvent)

- **`/root/antigravity-claude-proxy/`** — the Node proxy. Port its *business logic*
  verbatim: Anthropic↔Google format conversion (`src/format/`), account manager,
  multi-account rotation, rate-limit backoff, thinking-signature handling,
  schema sanitizer (incl. the `enum: [true]→["true"]` fix already applied),
  the `/anthropic` prefix alias, and the agy-token reader (`src/auth/agy-token.js`).
- **`/root/hermes-claude-auth/`** — Python reference for how a *Claude Code
  identity* proxy masks itself (billing header, system-prompt relocation, Stainless
  spoof). Not directly reused, but the *philosophy* of "match the real client
  byte-for-byte" is the same. Read `anthropic_billing_bypass.py` for the pattern.

## The proto problem (do this FIRST — everything depends on it)

The `v1internal.CloudCode` service is an internal Google API with **no public
.proto**. You must recover the FileDescriptorSet embedded in the `agy` Go binary.

1. Install tools: `go install github.com/arkadiyt/protodump/cmd/protodump@latest`
   and `pacman -S protobuf` (for `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc`).
2. `protodump -output ./proto /root/.local/bin/agy` — dumps embedded
   `FileDescriptorProto`s as `.proto` files.
3. Locate `google/internal/cloud/code/v1internal/*.proto`. Confirm it defines
   `CloudCode` with `GenerateContent`/`GenerateChat`/`StreamGenerateContent` and
   the request/response messages (cross-check names against `.reference/*.txt`).
4. `protoc --go_out=gen --go-grpc_out=gen <the .proto files>`.
5. If `protodump` misses fields, fall back to `strings`/manual descriptor parsing,
   but descriptors are almost always complete. **If the descriptor is genuinely
   unrecoverable, STOP and report** — do not fabricate a schema.

## Architecture

```
Hermes Agent ──HTTP──▶ Go proxy (:8091, Anthropic Messages API)
                          │  convert Anthropic → Cloud Code protobuf
                          ▼
                       grpc-go client ──h2/protobuf──▶ cloudcode-pa.googleapis.com
                          │  (crypto/tls default = agy JA4; gl-go x-goog-api-client)
                          ▼
                       convert Cloud Code protobuf → Anthropic response (SSE for streaming)
```

Keep the Node proxy running on :8090 as a fallback during development; the Go
proxy takes a **new port :8091** so both can coexist for A/B fingerprint testing.

## Phased build (commit after each phase; each phase must compile + be tested)

### Phase 0 — Scaffolding & proto recovery
- `go mod init antigravity-go-proxy`, Go 1.26.4, `google.golang.org/grpc@latest`.
- Recover proto (above). Commit generated Go stubs under `gen/`.
- **Gate:** generated `NewCloudCodeClient` compiles.

### Phase 1 — Auth (port from `src/auth/agy-token.js`)
- Read `~/.gemini/antigravity-cli/antigravity-oauth-token` (JSON: access_token,
  refresh_token, expiry, auth_method).
- Refresh via `https://oauth2.googleapis.com/token` with the shared client_id/secret
  (in the Node `src/constants.js` `OAUTH_CONFIG`). **File-lock** the token file to
  avoid clobbering a concurrently-running `agy` (Google rotates refresh_token on
  every refresh). Optional write-back gated by `AGY_TOKEN_WRITEBACK=1`.
- **Gate:** prints a fresh access_token + resolves account email.

### Phase 2 — gRPC client with agy fingerprint
- Dial `cloudcode-pa.googleapis.com:443` with
  `grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))`.
  **Empty `tls.Config{}`** — do NOT set CipherSuites/CurvePreferences/NextProtos.
- Attach per-call metadata exactly like agy: `x-goog-api-client: gl-go/1.26.4 ...`,
  `user-agent: antigravity/<ver> linux/amd64`, `x-client-name: antigravity`,
  `x-client-version`, plus the numeric client-metadata (ideType=9 ANTIGRAVITY,
  platform, pluginType=GEMINI). Pull exact header values from Node `src/constants.js`
  (`ANTIGRAVITY_HEADERS`, `CLIENT_METADATA`) and verify against the binary via
  `strings /root/.local/bin/agy | grep -iE 'antigravity/|x-goog-api|client-metadata'`.
- Endpoint fallback order (daily → prod) as in the Node proxy.
- **Gate (fingerprint):** capture your client's ClientHello with tcpdump and confirm
  `tls.handshake.ja4 == t13d1312h2...` matching `.reference/agy-capture.pcap`. This
  is the single most important acceptance test. If it doesn't match, the whole
  point is lost — debug before proceeding.

### Phase 3 — Format conversion (port from Node `src/format/`)
- Anthropic request → Cloud Code protobuf request (system-instruction relocation,
  `cache_control` stripping, tool/function-declaration mapping, the enum/const→string
  sanitizer fix, thinking config).
- Cloud Code response → Anthropic response (content blocks, thinking blocks +
  signatures, usage, stop_reason).
- **Gate:** unit tests converting a captured request/response round-trip.

### Phase 4 — Anthropic HTTP server
- `POST /v1/messages` (non-stream + SSE stream), `GET /v1/models`, `GET /health`.
- Mirror the `/anthropic` prefix alias (so Hermes' `base_url=.../anthropic`
  auto-detects `anthropic_messages` mode — same trick as the Node proxy).
- `x-api-key` auth for local access (reuse `agy-proxy-key-2026` or new).
- **Gate:** `curl /v1/messages` returns a valid Anthropic response through real gRPC.

### Phase 5 — Robustness (port from Node `message-handler.js`)
- Multi-account rotation, 429/quota backoff tiers, capacity-exhaustion retry,
  endpoint failover, permanent-auth-failure + ToS-ban detection (403 patterns).
- **Gate:** survives a forced 429 without crashing; rotates/rate-limits correctly.

### Phase 6 — Systemd + Hermes wiring
- `--no-webui`-equivalent is default (this build has no WebUI — pure API, ~small RAM).
- Write `antigravity-go-proxy.service` (PORT=8091). Do NOT auto-replace the running
  :8090 unit — leave both; the user flips Hermes over after verifying.
- Update Hermes `custom_providers` entry (or document the exact edit) to point
  `base_url` at `http://127.0.0.1:8091/anthropic`.
- **Gate:** `hermes chat -q "..." --provider custom:antigravity-proxy -m gemini-3.5-flash-low`
  returns a real answer through the Go proxy.

## Behavioral-mimicry stance (from deep research — scope decision)

Full behavioral parity (telemetry to `/api/event_logging/batch`, `cclog` uploads,
`onboardUser`/`loadCodeAssist` provisioning, language-server sidecar) is a
*secondary* signal. For THIS build:
- **DO** call `LoadCodeAssist`/`OnboardUser` on first run if required for project
  provisioning (the Node proxy already does — port it; a missing project → 403).
- **DO** send `RecordClientEvent`/minimal telemetry heartbeats if trivial to port.
- **DEFER** the language-server WebSocket sidecar (high effort, low marginal signal)
  — leave a clearly-commented `TODO(behavioral)` stub. Document this gap in README.

## Hard rules for the executor (Claude Code)

1. **Never override TLS knobs.** Empty `tls.Config{}`. No `utls`. No custom cipher
   lists. The Go default IS the disguise.
2. **Never fabricate the proto.** Recover it from the binary. If unrecoverable, stop
   and report — a wrong schema fails silently and looks like a ban.
3. **Verify fingerprint with real packet capture** at Phase 2 before building further.
4. **Port logic, don't redesign it.** The Node proxy's format/backoff/rotation logic
   is battle-tested — translate it faithfully to Go.
5. **New port 8091, new systemd unit.** Do not disturb the running :8090 Node proxy
   or its accounts.json until the user switches over.
6. **Commit per phase** with a clear message; keep each phase compiling.
7. **Read before writing:** `/root/antigravity-claude-proxy/src/` and `.reference/`
   are ground truth. Cross-check every constant against the `agy` binary.

## Acceptance (definition of done)

- [ ] `tls.handshake.ja4` of the Go proxy == `agy` (`t13d1312h2...`) — packet-verified.
- [ ] Traffic is gRPC/HTTP2/protobuf to `cloudcode-pa.googleapis.com` (not REST).
- [ ] `x-goog-api-client` starts `gl-go/`.
- [ ] `curl /v1/messages` and `/anthropic/v1/messages` return valid Anthropic JSON.
- [ ] Hermes `custom:antigravity-proxy` answers through the Go proxy end-to-end.
- [ ] systemd unit on :8091; Node :8090 untouched.
- [ ] README documents the deferred behavioral-sidecar gap.
