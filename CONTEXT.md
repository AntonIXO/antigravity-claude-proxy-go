# Antigravity Claude Proxy

A Go reverse proxy and translation bridge between Anthropic Messages API clients (such as Claude Code CLI) and Google Cloud Code / Gemini backends with multi-account rotation and transparent forwarding.

## Language

### Core Routing

**Translation Route**:
The processing path where Anthropic `/v1/messages` requests are translated to Google Cloud Code payloads and translated back to Anthropic SSE events.
_Avoid_: Emulation, conversion path, Google adapter.

**Transparent Forwarding**:
The direct reverse-proxy path bypassing translation for configured models, sending unmodified Anthropic requests to specified external endpoints.
_Avoid_: Passthrough mode, bypass route, proxy bypass.

**Custom Endpoint**:
A user-configured target host URL and optional API key mapped to specific model names for transparent forwarding.
_Avoid_: Upstream target, external provider, third-party backend.

### Identity & Account Management

**Account**:
A configured Google identity credential with quota metrics, rate-limit state, and subscription tier used to authenticate with Google Cloud Code.
_Avoid_: User, profile, client, credential pair.

**Account Source**:
The origin mechanism used to discover or supply an Account's credentials (`local_db`, `oauth`, or `manual`).
_Avoid_: Auth provider, login type, account origin.

**Selection Strategy**:
The algorithm (`sticky`, `round-robin`, or `hybrid`) determining which Account handles a Translation Route request.
_Avoid_: Load balancing mode, routing policy, account picker.

**Account Health**:
A dynamic numerical rating reflecting an Account's operational reliability, rewarded on successful requests and penalized on rate limits or errors.
_Avoid_: Score, reliability index, priority weight.

**Account Cooldown**:
A temporary lockout duration during which an Account is ineligible for selection following rate limit or capacity exhaustion errors.
_Avoid_: Backoff window, mute period, lockout timer.

### Models & Catalog

**Requested Model**:
The raw model name string provided in client API requests.
_Avoid_: Incoming model, user model, client string.

**Model Mapping**:
A configuration rule that rewrites a Requested Model to a target model name prior to routing or translation.
_Avoid_: Model alias, model override, model replacement.

**Upstream ID**:
The authoritative model identifier recognized by the Google Cloud Code backend.
_Avoid_: Internal model name, backend ID, Google model tag.

**Selectable Model**:
A model entry actively exposed and supported in the live Cloud Code catalog.
_Avoid_: Available model, active model, supported model.

### Format & Reasoning Adaptation

**Thinking Adaptation**:
The normalization of reasoning token budgets and synthesis/validation of thought signatures for reasoning-capable models.
_Avoid_: Thought parsing, reasoning hook, budget patch.

**Thought Signature Cache**:
An in-memory store mapping generated thought blocks to validation signatures required by Gemini thinking models across conversational turns.
_Avoid_: Signature map, thought storage, reasoning cache.

**Stream Translation**:
The real-time transformation of upstream SSE event streams into compliant Anthropic SSE format events.
_Avoid_: Stream conversion, event mapping, chunk forwarding.

### Error & Quota State

**Rate Limit Exceeded**:
A temporary request rate limit error (HTTP 429) carrying retry duration headers that triggers short account cooldown.
_Avoid_: Throttling, 429 error, rate block.

**Quota Exhausted**:
A depletion of model quota capacity (`remainingFraction` below threshold) that suspends an Account for that model until the quota reset timestamp.
_Avoid_: Usage limit, out of credits, quota burn.

**Model Capacity Exhausted**:
An upstream infrastructure overload condition (HTTP 503/529) that triggers exponential backoff and Account rotation.
_Avoid_: Server overload, Google capacity issue, backend down.
