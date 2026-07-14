# Format parity fixtures

These fixtures pin the Go conversion layer to the Node proxy under
`/root/antigravity-claude-proxy/src/`.

- `google-request.json` is the exact output of the Node
  `convertAnthropicToGoogle` function for `anthropic-request.json`.
- `anthropic-response.json` is the exact output of the Node
  `convertGoogleToAnthropic` function for `google-response.json`, after replacing
  its random message ID with `msg_fixture`.
- `anthropic-stream-events.json` is the exact output of the Node
  `streamSSEResponse` generator for `cloudcode-stream.sse`, after replacing its
  random message ID with `msg_stream`.

The Cloud Code shapes and field casing are retained as received; signature and
content strings are fixture values so no account token, project ID, or private
model output is committed.
