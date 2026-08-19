#!/usr/bin/env bash
set -euo pipefail

echo "=== Building Proxy ==="
go build -o bin/proxy ./cmd/proxy

echo "=== Running Full Test Suite ==="
go test -v ./...

echo "=== Running Performance Benchmarks ==="
./scripts/benchmark.sh

echo "=== Verification Complete ==="
