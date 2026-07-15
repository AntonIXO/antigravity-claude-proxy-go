# Project: antigravity-go-proxy

Go reimplementation of a Cloud Code proxy whose network fingerprint is
**identical to the official `agy` CLI** (a Go binary). Read `PLAN.md` first —
it is the authoritative roadmap. Execute it phase by phase.

## The one rule that matters most
This build is a disguise: a *normal Go gRPC client is the disguise*, because
`agy` is a normal Go gRPC client. **Do NOT touch TLS internals.** Use an empty
`tls.Config{}` through `grpc-go`. No `utls`, no custom `CipherSuites`, no custom
`CurvePreferences`, no custom `NextProtos`. Go 1.26.4 (already installed) +
default crypto/tls reproduces `agy`'s JA3/JA4 for free. Overriding anything
*breaks* the match.

## Ground truth (already gathered — don't re-derive)
- `.reference/grpc-methods.txt` — real `CloudCode/*` gRPC methods from the binary.
- `.reference/proto-messages.txt` — real `v1internal.*` protobuf message names.
- `.reference/agy-capture.pcap` — TARGET ClientHello (JA4 `t13d1312h2`).
- `.reference/proxy-capture.pcap` — the OLD Node proxy (JA4 `t13d5212h1`, avoid).

## Reference code (read, port faithfully — do not redesign)
- `/root/antigravity-claude-proxy/src/` — Node proxy. Source of business logic:
  `format/` (Anthropic↔Google conversion + schema sanitizer), `cloudcode/`
  (message-handler backoff/rotation), `constants.js` (`OAUTH_CONFIG`,
  `ANTIGRAVITY_HEADERS`, `CLIENT_METADATA`), `auth/agy-token.js` (token reader).
- `/root/hermes-claude-auth/anthropic_billing_bypass.py` — philosophy reference
  for byte-exact client masking (not reused directly).
- The `agy` binary: `/root/.local/bin/agy`. Cross-check every constant against it
  with `strings`.

## Key facts
- Go 1.26.4 installed. `protoc`/`protodump` are NOT — install in Phase 0.
- agy OAuth token: `~/.gemini/antigravity-cli/antigravity-oauth-token`.
  Never commit OAuth client credentials; inject the official installed-app
  values through the root-only service environment file.
- Target host: `cloudcode-pa.googleapis.com:443` (daily fallback: `daily-cloudcode-pa.googleapis.com`).
- New port **8091** (Node proxy owns 8090 — leave it running & untouched).
- Fingerprint gate command:
  `tshark -r <cap.pcap> -Y 'tls.handshake.type==1' -T fields -e tls.handshake.ja4`
  must equal `t13d1312h2...` for the Go proxy.

## Commands
- Build: `go build -o bin/proxy ./cmd/proxy`
- Proto: `protodump -output ./proto /root/.local/bin/agy` then `protoc --go_out=gen --go-grpc_out=gen ...`
- Fingerprint test: capture with `tcpdump -i any -w /tmp/go.pcap host cloudcode-pa.googleapis.com -c 30` while hitting the proxy, then run the tshark gate above.

## Definition of done
See `PLAN.md` "Acceptance". The non-negotiable one: **packet-verified JA4 match**.
Never fabricate the proto schema — if it can't be recovered from the binary, STOP
and report rather than inventing one (a wrong schema fails silently, looks like a ban).
