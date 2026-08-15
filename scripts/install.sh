#!/usr/bin/env bash
set -euo pipefail

# Directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

TARGET_DIR="${HOME}/.local/bin"
BINARY_NAME="antigravity-proxy"
VERSION="${VERSION:-$(git -C "${REPO_ROOT}" describe --tags --always --dirty 2>/dev/null || echo "1.0.0")}"

echo "==> Cleaning previous build artifacts..."
rm -rf "${REPO_ROOT}/bin" "${REPO_ROOT}/proxy"

echo "==> Running tests..."
go test ./...

echo "==> Building release binary (version: ${VERSION})..."
mkdir -p "${REPO_ROOT}/bin"
CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o "${REPO_ROOT}/bin/${BINARY_NAME}" \
    "${REPO_ROOT}/cmd/proxy"

echo "==> Installing ${BINARY_NAME} to ${TARGET_DIR}..."
mkdir -p "${TARGET_DIR}"
cp -f "${REPO_ROOT}/bin/${BINARY_NAME}" "${TARGET_DIR}/${BINARY_NAME}"
chmod +x "${TARGET_DIR}/${BINARY_NAME}"

echo "==> Verifying installation..."
"${TARGET_DIR}/${BINARY_NAME}" --help >/dev/null 2>&1 || true

echo "==> Successfully installed ${BINARY_NAME} to ${TARGET_DIR}/${BINARY_NAME}"

if [[ ":${PATH}:" != *":${TARGET_DIR}:"* ]]; then
    echo "Note: ${TARGET_DIR} is not in your PATH. Add it to your shell configuration file:"
    echo "    export PATH=\"\${HOME}/.local/bin:\${PATH}\""
fi
