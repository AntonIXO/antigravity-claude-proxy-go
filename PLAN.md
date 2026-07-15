# PLAN — Current-`agy` Go REST/SSE Antigravity Proxy

## Mission

Reimplement the Node.js `antigravity-claude-proxy` as a **Go** binary whose
Cloud Code traffic matches the currently installed official `agy` CLI. The
proxy exposes a local Anthropic Messages API (`/v1/messages`) for Hermes Agent
and calls Google Cloud Code over the same HTTPS REST/SSE transport as agy.

The initial roadmap assumed agy used gRPC. A fresh packet capture and an agy
application log taken on 2026-07-14 disproved that assumption. A second run
explicitly selecting `Claude Sonnet 4.6 (Thinking)` confirmed this is not a
Gemini-only behavior. Current agy 1.1.2 calls endpoints such as:

- `https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist`
- `https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels`
- `https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse`

Network fidelity therefore means native Go `net/http`, HTTP/1.1 JSON/SSE, and
the current Cloud Code ClientHello—not grpc-go.

## Packet-verified ground truth

| Vector | Old Node proxy | Current agy 1.1.2 (Gemini and Claude) | Go proxy target |
|---|---|---|---|
| Application protocol | REST/SSE | REST/SSE | **REST/SSE** |
| Cloud Code ALPN | `http/1.1`/OpenSSL behavior | absent | **absent** |
| Cloud Code JA4 | `t13d5212h1...` | `t13d131100_f57a46bbacb6_f50d94e863eb` | **exact match** |
| TLS implementation | Node/OpenSSL | internal Go 1.27 RC/BoringCrypto | **public Go 1.27 RC `crypto/tls`** |
| Content endpoint | `v1internal:generateContent` | `v1internal:streamGenerateContent?alt=sse` | **same** |
| API identity | historically Node-flavored | `gl-go/...`, Antigravity headers | **same** |

The older `.reference/agy-capture.pcap` value
`t13d1312h2_f57a46bbacb6_f50d94e863eb` is a `www.googleapis.com` `net/http`
connection with ALPN `h2,http/1.1`; it is not a Cloud Code connection and is no
longer the gate.

The authoritative baseline is:

- `.reference/agy-current-capture.pcap`
- `.reference/agy-current-baseline.txt`
- SHA-256 `2d041c7f794c5ec018543c2f5b953ecaa2bf5855c69670b522b696e68c0f6ca9`
- `.reference/agy-claude-current-capture.pcap`
- `.reference/agy-claude-current-baseline.txt`
- Claude-capture SHA-256
  `89112ac5f3d7075b976d5be8fdd9bf6f23202ac532a5712baddddf403420f527`
- Cloud Code JA4 `t13d131100_f57a46bbacb6_f50d94e863eb`

The Gemini and Claude captures use the same Cloud Code SNI, omit ALPN, have the
same exact JA4, and log the same `streamGenerateContent?alt=sse` generation
path. Model selection therefore does not change the transport architecture in
the currently installed CLI.

Verify captures with:

```sh
tshark -r <cap.pcap> \
  -Y 'tls.handshake.type==1 && tls.handshake.extensions_server_name contains "cloudcode"' \
  -T fields -e tls.handshake.extensions_server_name \
  -e tls.handshake.extensions_alpn_str -e tls.handshake.ja4
```

## TLS rule

Do not customize TLS internals. Use an empty `tls.Config{}` with Go's standard
HTTP transport. Do not set `CipherSuites`, `CurvePreferences`, `NextProtos`,
minimum/maximum TLS versions, or a custom ClientHello. Do not use `utls`.

Current agy was built with an internal
`go1.27-20260710-RC00 ... X:fieldtrack,boringcrypto,simd` toolchain. Public Go
1.27rc2 has the same signature-algorithm extension hash in the captured
ClientHello. Use the closest public Go 1.27 release candidate and packet-verify
the complete JA4. Toolchain selection is part of the fingerprint gate.

For Cloud Code, use a dedicated `http.Transport` with only
`TLSClientConfig: &tls.Config{}`. Leave `ForceAttemptHTTP2` at its zero value;
that matches agy's observed lack of ALPN. Do not reuse a transport that enables
automatic HTTP/2.

## Reference implementations

- `/root/antigravity-claude-proxy/src/` is the business-logic source. Port its
  format conversion, schema sanitizer, thinking signatures, request envelope,
  SSE parsing, account rotation, and backoff faithfully.
- `/root/hermes-claude-auth/anthropic_billing_bypass.py` is the masking
  philosophy reference.
- `/root/.local/bin/agy` and the current capture/log are the network ground truth.
- Recovered schemas under `proto/` and generated types under `gen/` remain
  useful for validating JSON field names and enums. Do not invent schema.

## Architecture

```text
Hermes Agent ──HTTP──▶ Go proxy (:8091, Anthropic Messages API)
                          │  Anthropic → agy-compatible JSON envelope
                          ▼
                     Go net/http client ──HTTP/1.1 JSON/SSE──▶ Cloud Code
                          │  empty tls.Config; current-agy headers and JA4
                          ▼
                     SSE/JSON → Anthropic response/events
```

The Node proxy remains running and untouched on port 8090. The Go proxy uses
port 8091 until the user explicitly switches Hermes.

## Phased build

Commit after each phase. Every phase must compile and pass its gate before the
next begins.

### Phase 0 — Scaffolding and exact schema recovery — COMPLETE

- Initialize the Go module and install protobuf recovery/generation tools.
- Recover descriptors from agy without fabricating missing fields.
- Generate compileable Go schema/service types under `gen/`.
- **Gate:** recovered `NewCloudCodeClient` and `NewPredictionServiceClient`
  compile.
- Commit: `27c9332`.

The recovered schema also established that `PredictionService`, not
`CloudCode`, owns `GenerateContent`. Generated gRPC stubs are retained as schema
evidence but are not the current-agy transport.

### Phase 1 — Auth — COMPLETE

- Read wrapped and flat agy token files.
- File-lock refresh, handle a rotated refresh token, and gate optional atomic
  write-back behind `AGY_TOKEN_WRITEBACK=1`.
- Resolve the account email using Google userinfo.
- **Gate:** a real expired token refresh succeeds and resolves the email.
- Commit: `48b95d8`.

### Baseline correction — COMPLETE

- Capture a clean current-agy one-shot request to Cloud Code.
- Repeat the capture while explicitly selecting a Claude thinking model so the
  transport decision is not inferred from Gemini mode alone.
- Preserve the pcap and provenance under `.reference/`.
- Confirm from agy's own log that generation uses REST/SSE.
- Commit: `52ec2b8`.

### Phase 2 — Native Go REST/SSE client with current-agy fingerprint

- Select public Go 1.27rc2 (or a closer available public 1.27 build) because Go
  1.26.4 lacks the three signature algorithms present in current agy's
  ClientHello.
- Call daily Cloud Code first, then production fallback, with standard
  `net/http` and a dedicated `http.Transport{TLSClientConfig: &tls.Config{}}`.
- Leave ALPN/HTTP2 controls untouched at zero values.
- Send agy identity headers: `Authorization`, `User-Agent`, `X-Client-Name`,
  `X-Client-Version`, and `x-goog-api-client: gl-go/1.26.4 auth/0.5
  google-api-go-client/0.5`.
- Implement unary JSON and streaming SSE request primitives for
  `loadCodeAssist`, `onboardUser`, `fetchAvailableModels`, `generateContent`,
  and `streamGenerateContent`.
- **Gate:** a real Cloud Code call succeeds, and a tcpdump of the Go client has
  SNI `daily-cloudcode-pa.googleapis.com`, no ALPN, and exact JA4
  `t13d131100_f57a46bbacb6_f50d94e863eb`.

### Phase 3 — Format conversion

- Port Anthropic request → agy JSON request-envelope conversion.
- Preserve system-instruction relocation, `cache_control` stripping,
  tool/function mapping, enum/const-to-string schema sanitization, thinking
  configuration, and thought signatures.
- Port Cloud Code JSON/SSE responses → Anthropic content/thinking/tool blocks,
  usage, and stop reason.
- **Gate:** captured request/response fixtures pass deterministic conversion and
  streaming round-trip tests.

### Phase 4 — Anthropic HTTP server

- Serve `POST /v1/messages`, `GET /v1/models`, and `GET /health`.
- Support non-streaming JSON and Anthropic SSE streaming.
- Mirror every route under `/anthropic` for Hermes provider detection.
- Require local `x-api-key` authentication.
- **Gate:** both `/v1/messages` and `/anthropic/v1/messages` return valid real
  Anthropic responses through the Go REST/SSE client.

### Phase 5 — Robustness

- Port Node account loading without modifying the live Node `accounts.json`.
- Port selection strategy, per-model limits, 429/quota backoff tiers,
  capacity-exhaustion retry, endpoint failover, permanent auth failure, and
  ToS/verification 403 detection.
- **Gate:** deterministic forced-429 tests prove cooldown and rotation, followed
  by a successful real request.

### Phase 6 — Systemd and Hermes

- Add a new `antigravity-go-proxy.service` on port 8091.
- Leave the Node service and port 8090 untouched.
- Point Hermes `custom:antigravity-proxy` at
  `http://127.0.0.1:8091/anthropic` only after all earlier gates pass.
- Run Claude Code against the same `/anthropic` base URL with its Sonnet and
  Opus model environment variables explicitly forced to model IDs returned by
  the Go proxy. Keep the test environment isolated from the user's normal
  Claude configuration.
- **Gate:** `hermes chat -q "..." --provider custom:antigravity-proxy -m
  gemini-3.5-flash-low` returns a real answer through the Go proxy, and Claude
  Code returns real answers through each forced model mapping.

### Post-acceptance model catalog correction — COMPLETE

- Use `agentModelSorts` from `fetchAvailableModels` as the authoritative list
  behind `/v1/models`, matching the eight entries printed by `agy models`.
- Preserve Cloud Code's routing IDs and display labels, including the
  counterintuitive `gemini-3.5-flash-low` → Medium mapping.
- Route the compatibility input `gemini-3.1-pro-high` to the selectable
  `gemini-pro-agent` ID used for Gemini 3.1 Pro High.
- Apply live `supportsThinking`, `thinkingBudget`, `minThinkingBudget`, context,
  and maximum-output metadata while building requests.
- **Gate:** all eight advertised IDs and the Pro High compatibility alias
  return HTTP 200; Hermes' exact 182 KB Opus request succeeds after its
  requested 128,000 output tokens are capped to Cloud Code's live 64,000 limit.

### Post-acceptance usage integration and fingerprint recheck — COMPLETE

- Add authenticated `GET /v1/usage` and `/anthropic/v1/usage` routes backed by
  Cloud Code's live `fetchAvailableModels` quota data.
- Preserve every selectable model's `remainingFraction` and `resetTime` while
  grouping only identical quota buckets for display. Gemini and Anthropic
  usage remain separate; GPT-OSS currently shares Anthropic's exact bucket.
- Extend Hermes' existing account-usage abstraction so custom providers may
  expose an optional `/v1/usage` endpoint. The integration is fail-open for
  custom providers that do not implement it.
- **Gate:** Hermes' real gateway `/usage` handler renders separate `Gemini
  quota` and `Anthropic / GPT-OSS quota` lines from the deployed proxy.
- **Fingerprint gate:** fresh live captures of both `agy 1.1.2` and the
  deployed Go `/v1/usage` request again match exact JA4
  `t13d131100_f57a46bbacb6_f50d94e863eb`, daily Cloud Code SNI, absent ALPN,
  cipher list, and signature algorithms. See
  `.reference/fingerprint-recheck-20260715.txt`.

### Post-acceptance credential and service cleanup — COMPLETE

- Remove OAuth client credentials from source and inject them through the
  root-only `/etc/antigravity-go-proxy.env` file.
- Rewrite the unpublished local Git history so push protection cannot find the
  former credential values in any commit.
- Remove the obsolete Node systemd unit after explicit user approval while
  retaining its `accounts.json` as the Go proxy's read-only account source.
- **Gate:** unit, race, and vet checks pass; a real `/v1/messages` call and an
  end-to-end Hermes request both succeed after restarting the Go service.

## Behavioral-mimicry scope

- **Do** use `loadCodeAssist` and `onboardUser` when project provisioning needs
  them.
- **Do** use the exact REST paths and SSE query observed in current agy.
- **Do** preserve current-agy client metadata and request-envelope structure.
- **Do** add minimal `RecordClientEvent` behavior if it is straightforward after
  the primary path is accepted.
- **Defer** the language-server sidecar and document the gap in README as
  `TODO(behavioral)`.

## Hard rules

1. Never override TLS knobs; use an empty `tls.Config{}`.
2. The current Cloud Code capture, not the older `www.googleapis.com` frame, is
   the packet gate.
3. Never claim Phase 2 passed without a real tcpdump/tshark exact JA4 match.
4. Never fabricate protobuf/JSON schema.
5. Port Node business logic rather than redesigning it.
6. Use port 8091 and never modify or delete the configured accounts file.
7. Commit each completed phase with its real gate output.

## Acceptance

- [x] Go Cloud Code JA4 equals current agy exactly:
      `t13d131100_f57a46bbacb6_f50d94e863eb`, packet-verified.
- [x] Cloud Code SNI is correct and ALPN is absent, matching current agy.
- [x] Traffic uses the HTTPS REST/SSE paths observed for both Gemini and Claude,
      including `streamGenerateContent?alt=sse`.
- [x] `x-goog-api-client` begins with `gl-go/` and identity headers match agy.
- [x] `/v1/messages` and `/anthropic/v1/messages` return valid Anthropic JSON.
- [x] Streaming emits valid Anthropic SSE events.
- [x] Forced 429 handling rotates/cools down without crashing.
- [x] Hermes answers end-to-end through `custom:antigravity-proxy`.
- [x] Claude Code answers end-to-end with Sonnet and Opus forced to the Go
      proxy's advertised model IDs.
- [x] Go systemd unit runs on 8091; the obsolete Node unit is removed while its
      account file remains available read-only to the Go proxy.
- [x] README documents current baseline evidence and the deferred sidecar gap.
- [x] `/v1/models` matches agy's complete selectable agent list and excludes
      non-agent raw routes.
- [x] Every advertised model returns a real response, and Hermes Opus requests
      respect the live 64,000-token output cap instead of failing HTTP 400.
- [x] `/v1/usage` returns live, separate Gemini and Anthropic quota windows.
- [x] Hermes `/usage` renders the Go proxy's Cloud Code account limits.
- [x] A fresh live agy/proxy packet recheck still matches the complete JA4,
      SNI, ALPN state, cipher suites, and signature algorithms.
