#!/usr/bin/env bash
set -euo pipefail

echo "=== Running Go Benchmarks ==="
go test -v -benchmem -bench=. ./internal/format ./internal/cloudcode | tee benchmark_results.txt
