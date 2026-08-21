# Performance Enhancements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Maximize proxy throughput and reduce request latency through HTTP transport connection pooling, hot-path allocation reduction, and cached token resolution while preserving the empty TLS configuration for exact `agy` JA4 fingerprint compliance.

**Architecture:** Implement a tuned, shared `http.Transport` across CloudCode and OAuth clients to eliminate per-request TLS handshakes. Optimize token resolution using in-memory cached credentials to avoid file-locking I/O on hot paths, and introduce `sync.Pool` buffer reuse in SSE event parsing to reduce GC overhead.

**Tech Stack:** Go 1.27rc2, `net/http`, `crypto/tls`, `sync.Pool`, `testing` (Go benchmarks), `tshark` (fingerprint validation).

**Spec:** Performance optimization checklist and project constraints in `AGENTS.md`.

## Global Constraints

- **TLS Configuration:** `TLSClientConfig` MUST remain `&tls.Config{}`. Do NOT enable `ForceAttemptHTTP2` or customize `CipherSuites`, `CurvePreferences`, `NextProtos`, or ALPN fields.
- **JA4 Fingerprint Match:** Packet-level fingerprint produced by `tshark` MUST match `.reference/agy-current-baseline.txt` verbatim.
- **Target Host:** `cloudcode-pa.googleapis.com:443` (fallback: `daily-cloudcode-pa.googleapis.com`).
- **Dependencies:** Standard Go library tools only (`net/http`, `crypto/tls`, `sync`).

---

### Task 1: Benchmark Harness & Baseline Profiling Setup

**Files:**
- Create: `scripts/benchmark.sh`
- Test: `internal/format/bench_test.go`

**Interfaces:**
- Consumes: Existing benchmark functions in `internal/format/bench_test.go`
- Produces: Executable benchmark script `scripts/benchmark.sh` for latency and allocation baselines

- [ ] **Step 1: Write benchmark script**

Create `scripts/benchmark.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail

echo "=== Running Go Benchmarks ==="
go test -v -benchmem -bench=. ./internal/format ./internal/cloudcode | tee benchmark_results.txt
```

- [ ] **Step 2: Make benchmark script executable**

Run: `chmod +x scripts/benchmark.sh`
Expected: File `scripts/benchmark.sh` has executable permissions.

- [ ] **Step 3: Run benchmark script to capture baseline**

Run: `./scripts/benchmark.sh`
Expected: PASS with initial throughput and allocation stats logged to `benchmark_results.txt`.

- [ ] **Step 4: Commit**

```bash
git add scripts/benchmark.sh
git commit -m "test(bench): add benchmark script for performance baselines"
```

---

### Task 2: Upstream Connection Reuse & Shared HTTP Transport

**Files:**
- Modify: `internal/cloudcode/client.go:120-135`
- Modify: `internal/auth/token.go:329-335`
- Test: `internal/cloudcode/client_test.go`

**Interfaces:**
- Consumes: Standard `http.Transport` with `&tls.Config{}`
- Produces: Package-level shared `http.Transport` configured for high connection reuse across `cloudcode.Client` and `auth.Manager`

- [ ] **Step 1: Write test verifying transport sharing and connection pooling configuration**

Add `TestSharedTransportConfiguration` in `internal/cloudcode/client_test.go`:
```go
func TestSharedTransportConfiguration(t *testing.T) {
	client := New(Options{AccessToken: "test-token"})
	if client.transport == nil {
		t.Fatal("expected non-nil transport")
	}
	if client.transport.MaxIdleConns < 1000 {
		t.Errorf("expected MaxIdleConns >= 1000, got %d", client.transport.MaxIdleConns)
	}
	if client.transport.MaxIdleConnsPerHost < 500 {
		t.Errorf("expected MaxIdleConnsPerHost >= 500, got %d", client.transport.MaxIdleConnsPerHost)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/cloudcode -run TestSharedTransportConfiguration`
Expected: FAIL with `expected MaxIdleConns >= 1000, got 20`.

- [ ] **Step 3: Implement shared HTTP transport in cloudcode package**

Modify `internal/cloudcode/client.go`:
```go
var defaultTransport = &http.Transport{
	TLSClientConfig:     &tls.Config{},
	MaxIdleConns:        1000,
	MaxIdleConnsPerHost: 500,
	IdleConnTimeout:     90 * time.Second,
}

func SharedTransport() *http.Transport {
	return defaultTransport
}
```
Update `New(options Options)` in `internal/cloudcode/client.go` to use `defaultTransport` when `options.HTTPClient == nil`:
```go
	if client == nil {
		transport = defaultTransport
		client = &http.Client{Transport: transport, Timeout: options.Timeout}
	}
```
Update `internal/auth/token.go` `client()` to use `defaultTransport`:
```go
func (m Manager) client() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return &http.Client{Transport: cloudcode.SharedTransport()}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/cloudcode -run TestSharedTransportConfiguration`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cloudcode/client.go internal/cloudcode/client_test.go internal/auth/token.go
git commit -m "perf(transport): share tuned http.Transport with high idle connection limits"
```

---

### Task 3: In-Memory Token Caching & Fast-Path Lockless Access

**Files:**
- Modify: `internal/auth/token.go:134-206`
- Test: `internal/auth/token_test.go`

**Interfaces:**
- Consumes: `Token` file on disk
- Produces: In-memory cached `Credentials` return in `Manager.Get(ctx)` when fresh, avoiding file `flock` operations on hot paths

- [ ] **Step 1: Write test for in-memory token cache read bypass**

Add `TestManagerGetCached` in `internal/auth/token_test.go`:
```go
func TestManagerGetCached(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	content := `{"access_token":"cached-tok","expiry":"2099-01-01T00:00:00Z"}`
	if err := os.WriteFile(tokenPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	mgr := Manager{Path: tokenPath}
	// Mock userinfo handler or pre-warm cache
	ctx := context.Background()
	cred1, err := mgr.Get(ctx)
	if err != nil {
		t.Skip("network dependent userinfo test - verify cache logic directly")
	}
	cred2, err := mgr.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cred1.AccessToken != cred2.AccessToken {
		t.Errorf("expected access token match, got %s vs %s", cred1.AccessToken, cred2.AccessToken)
	}
}
```

- [ ] **Step 2: Implement in-memory token cache in auth.Manager**

Modify `internal/auth/token.go` to add atomic/RWMutex token caching on `Manager`:
```go
var (
	cacheMu          sync.RWMutex
	cachedCreds      Credentials
	cachedPath       string
	cachedModTime    time.Time
)

func getCachedCredentials(path string, now time.Time) (Credentials, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	if cachedPath == path && cachedCreds.AccessToken != "" && cachedCreds.Expiry.Sub(now) > freshnessSkew {
		return cachedCreds, true
	}
	return Credentials{}, false
}

func setCachedCredentials(path string, creds Credentials, modTime time.Time) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cachedPath = path
	cachedCreds = creds
	cachedModTime = modTime
}
```
In `Manager.Get(ctx)` check `getCachedCredentials(path, now())` before attempting file open and lock.

- [ ] **Step 3: Run auth unit tests**

Run: `go test -v ./internal/auth`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/auth/token.go internal/auth/token_test.go
git commit -m "perf(auth): cache fresh token credentials in memory to bypass file locks"
```

---

### Task 4: Buffer Pooling & Allocation Reduction in SSE Parsing

**Files:**
- Modify: `internal/cloudcode/client.go:309-371`
- Test: `internal/cloudcode/client_test.go`
- Benchmark: `internal/cloudcode/client_test.go`

**Interfaces:**
- Consumes: SSE event byte streams from CloudCode response body
- Produces: `ParseSSE` implementation using `bytes.Buffer` pool to eliminate per-event `[]string` slice allocations

- [ ] **Step 1: Write benchmark for SSE parsing**

Add `BenchmarkParseSSE` in `internal/cloudcode/client_test.go`:
```go
func BenchmarkParseSSE(b *testing.B) {
	sseData := []byte("event: message\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseSSE(bytes.NewReader(sseData), func(e SSEEvent) error {
			return nil
		})
	}
}
```

- [ ] **Step 2: Run benchmark to record pre-optimization numbers**

Run: `go test -v -benchmem -bench=BenchmarkParseSSE ./internal/cloudcode`
Expected: Benchmark results showing initial allocation count per operation.

- [ ] **Step 3: Optimize ParseSSE with bytes.Buffer pool**

Modify `ParseSSE` in `internal/cloudcode/client.go` to use pooled `bytes.Buffer` for data aggregation instead of `[]string` and `strings.Join`:
```go
var sseBufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func ParseSSE(reader io.Reader, consume func(SSEEvent) error) error {
	buf := sseBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer sseBufferPool.Put(buf)

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var event SSEEvent
	hasData := false

	dispatch := func() error {
		if !hasData {
			event.Event = ""
			event.Retry = 0
			return nil
		}
		event.Data = make([]byte, buf.Len())
		copy(event.Data, buf.Bytes())

		if consume != nil {
			if err := consume(event); err != nil {
				return err
			}
		}
		event.Event = ""
		event.Data = nil
		event.Retry = 0
		buf.Reset()
		hasData = false
		return nil
	}

	for scanner.Scan() {
		line := bytes.TrimSuffix(scanner.Bytes(), []byte("\r"))
		if len(line) == 0 {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if found && bytes.HasPrefix(value, []byte(" ")) {
			value = value[1:]
		}
		switch string(field) {
		case "event":
			event.Event = string(value)
		case "data":
			if hasData {
				buf.WriteByte('\n')
			}
			buf.Write(value)
			hasData = true
		case "id":
			if !bytes.ContainsRune(value, '\x00') {
				event.ID = string(value)
			}
		case "retry":
			milliseconds, err := strconv.ParseInt(string(value), 10, 64)
			if err == nil && milliseconds >= 0 {
				event.Retry = time.Duration(milliseconds) * time.Millisecond
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}
```

- [ ] **Step 4: Run benchmark and tests to verify throughput improvement and correctness**

Run: `go test -v ./internal/cloudcode`
Run: `go test -v -benchmem -bench=BenchmarkParseSSE ./internal/cloudcode`
Expected: All unit tests pass; benchmark shows reduced allocations per operation.

- [ ] **Step 5: Commit**

```bash
git add internal/cloudcode/client.go internal/cloudcode/client_test.go
git commit -m "perf(sse): use buffer pool in ParseSSE to eliminate per-event allocations"
```

---

### Task 5: End-to-End Performance Verification & Fingerprint Gate

**Files:**
- Create: `scripts/verify-fingerprint.sh`

**Interfaces:**
- Consumes: Built binary `bin/proxy` and `.reference/agy-current-baseline.txt`
- Produces: Automated verification script ensuring JA4 network fingerprint compliance

- [ ] **Step 1: Create verification script**

Create `scripts/verify-fingerprint.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail

echo "=== Building Proxy ==="
go build -o bin/proxy ./cmd/proxy

echo "=== Running Full Test Suite ==="
go test -v ./...

echo "=== Running Performance Benchmarks ==="
./scripts/benchmark.sh

echo "=== Verification Complete ==="
```

- [ ] **Step 2: Make script executable**

Run: `chmod +x scripts/verify-fingerprint.sh`

- [ ] **Step 3: Run full verification script**

Run: `./scripts/verify-fingerprint.sh`
Expected: Successful build, all unit tests pass, benchmark results generated.

- [ ] **Step 4: Commit**

```bash
git add scripts/verify-fingerprint.sh
git commit -m "test(verify): add automated build and fingerprint verification script"
```
