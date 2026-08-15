BINARY_NAME=antigravity-proxy
BIN_DIR=bin
VERSION?=1.0.0
LDFLAGS=-s -w -X main.version=$(VERSION)

.PHONY: all build test clean cross-compile dist install

all: build

build:
	@mkdir -p $(BIN_DIR)
	@echo "Building $(BINARY_NAME) for current platform..."
	go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME) ./cmd/proxy

test:
	@echo "Running tests..."
	go test -v -race ./...

clean:
	@echo "Cleaning up..."
	@rm -rf $(BIN_DIR) proxy

cross-compile: dist

dist:
	@mkdir -p $(BIN_DIR)
	@echo "Building cross-platform release binaries..."

	@echo "  -> darwin/arm64..."
	@GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/proxy

	@echo "  -> darwin/amd64..."
	@GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/proxy

	@echo "  -> linux/amd64..."
	@GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/proxy

	@echo "  -> linux/arm64..."
	@GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/proxy

	@echo "  -> windows/amd64..."
	@GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME)-windows-amd64.exe ./cmd/proxy

	@echo "All binaries built in $(BIN_DIR)/:"
	@ls -la $(BIN_DIR)/

install:
	@./scripts/install.sh
