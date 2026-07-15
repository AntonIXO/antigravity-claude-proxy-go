# Format parity fixtures

These fixtures pin the conversion layer's expected request and response
shapes.

- `google-request.json` is the expected conversion of `anthropic-request.json`.
- `anthropic-response.json` is the expected conversion of
  `google-response.json`, after replacing its random message ID with
  `msg_fixture`.
- `anthropic-stream-events.json` is the expected SSE conversion of
  `cloudcode-stream.sse`, after replacing its random message ID with
  `msg_stream`.

The Cloud Code shapes and field casing are retained as received; signature and
content strings are fixture values so no account token, project ID, or private
model output is committed.
