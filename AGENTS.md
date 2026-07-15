# Project: antigravity-go-proxy

Go Cloud Code proxy whose network fingerprint is **identical to the official
`agy` CLI**.

## The one rule that matters most
This build is a disguise: a *normal Go HTTPS client is the disguise*, because
that is how current `agy` reaches Cloud Code. **Do NOT touch TLS internals.**
Use an empty `tls.Config{}` through the standard HTTP transport. No `utls`, no
custom `CipherSuites`, no custom `CurvePreferences`, no custom `NextProtos`.
Go 1.27rc2 + default crypto/tls reproduces `agy`'s JA3/JA4 for free.
Overriding anything *breaks* the match.

## Ground truth (already gathered — don't re-derive)
- `.reference/grpc-methods.txt` — real `CloudCode/*` gRPC methods from the binary.
- `.reference/proto-messages.txt` — real `v1internal.*` protobuf message names.
- `.reference/agy-current-capture.pcap` — current Cloud Code ClientHello.
- `.reference/agy-current-baseline.txt` — current `agy` fingerprint baseline.
- `.reference/go-current-baseline.txt` — matching Go proxy baseline.

## Reference code (read, port faithfully — do not redesign)
- `/root/antigravity-claude-proxy/src/` — historical business-logic reference:
  `format/` (Anthropic↔Google conversion + schema sanitizer), `cloudcode/`
  (message-handler backoff/rotation), `constants.js` (request identity), and
  `auth/agy-token.js` (token reader).
- `/root/hermes-claude-auth/anthropic_billing_bypass.py` — philosophy reference
  for byte-exact client masking (not reused directly).
- The `agy` binary: `/root/.local/bin/agy`. Cross-check every constant against it
  with `strings`.

## Key facts
- Go 1.27rc2 installed. `protoc`/`protodump` are NOT required for normal proxy
  development.
- agy OAuth token: `~/.gemini/antigravity-cli/antigravity-oauth-token`.
  Never commit OAuth client credentials; obtain refresh values from the
  installed `agy` executable only when a refresh is needed.
- Target host: `cloudcode-pa.googleapis.com:443` (daily fallback: `daily-cloudcode-pa.googleapis.com`).
- Local port **8091**.
- Fingerprint gate command:
  `tshark -r <cap.pcap> -Y 'tls.handshake.type==1' -T fields -e tls.handshake.ja4`
  must match `.reference/agy-current-baseline.txt` for the Go proxy.

## Commands
- Build: `go build -o bin/proxy ./cmd/proxy`
- Proto: `protodump -output ./proto /root/.local/bin/agy` then `protoc --go_out=gen --go-grpc_out=gen ...`
- Fingerprint test: capture with `tcpdump -i any -w /tmp/go.pcap host cloudcode-pa.googleapis.com -c 30` while hitting the proxy, then run the tshark gate above.

## Definition of done

The non-negotiable requirement is **packet-verified JA4 match**. Never
fabricate the proto schema — if it can't be recovered from the binary, stop and
report rather than inventing one (a wrong schema fails silently, looks like a
ban).
