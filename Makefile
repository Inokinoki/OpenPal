# openpal Makefile
# Common operations for development and deployment

.PHONY: help build build-ws-test test clean install run fmt lint vet race coverage deps version

# Variables
BINARY_NAME=openpal
WS_TEST_NAME=ws-test
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GO_VERSION=$(shell go version | awk '{print $$3}')

# Build flags
LDFLAGS=-ldflags "-s -w -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME} -X main.gitCommit=${GIT_COMMIT}"

# Default target
help:
	@echo "openpal Makefile"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build       Build the binary (default)"
	@echo "  build-ws-test  Build WebSocket test CLI"
	@echo "  test        Run tests"
	@echo "  clean       Clean build artifacts"
	@echo "  install     Install binary to /usr/local/bin"
	@echo "  run         Run openpal (requires arguments)"
	@echo "  fmt         Format code"
	@echo "  lint        Run linter"
	@echo "  vet         Run go vet"
	@echo "  race        Run tests with race detector"
	@echo "  coverage    Generate coverage report"
	@echo "  deps        Update dependencies"
	@echo "  version     Show version info"
	@echo "  all         Run build, test, lint, vet"
	@echo ""

# Build the binary
build:
	@echo "Building ${BINARY_NAME}..."
	@echo "Version: ${VERSION}"
	@echo "Go: ${GO_VERSION}"
	@echo "Commit: ${GIT_COMMIT}"
	@echo ""
	CGO_ENABLED=0 go build ${LDFLAGS} -o ${BINARY_NAME} ./cmd/openpal
	@echo ""
	@echo "✅ Build complete: ./${BINARY_NAME}"
	@ls -lh ${BINARY_NAME}

# Build WebSocket test CLI
build-ws-test:
	@echo "Building ${WS_TEST_NAME}..."
	CGO_ENABLED=0 go build -o ${WS_TEST_NAME} ./cmd/ws-test
	@echo ""
	@echo "✅ Build complete: ./${WS_TEST_NAME}"
	@ls -lh ${WS_TEST_NAME}

# Build for specific platform
build-linux:
	@echo "Building for Linux (amd64)..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ${LDFLAGS} -o ${BINARY_NAME}-linux-amd64 ./cmd/openpal
	@echo "✅ Built: ./${BINARY_NAME}-linux-amd64"

build-linux-arm64:
	@echo "Building for Linux (arm64)..."
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ${LDFLAGS} -o ${BINARY_NAME}-linux-arm64 ./cmd/openpal
	@echo "✅ Built: ./${BINARY_NAME}-linux-arm64"

build-darwin:
	@echo "Building for macOS (amd64)..."
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ${LDFLAGS} -o ${BINARY_NAME}-darwin-amd64 ./cmd/openpal
	@echo "✅ Built: ./${BINARY_NAME}-darwin-amd64"

build-darwin-arm64:
	@echo "Building for macOS (arm64)..."
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ${LDFLAGS} -o ${BINARY_NAME}-darwin-arm64 ./cmd/openpal
	@echo "✅ Built: ./${BINARY_NAME}-darwin-arm64"

build-windows:
	@echo "Building for Windows (amd64)..."
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ${LDFLAGS} -o ${BINARY_NAME}-windows-amd64.exe ./cmd/openpal
	@echo "✅ Built: ./${BINARY_NAME}-windows-amd64.exe"

# Build all platforms
build-all: build-linux build-linux-arm64 build-darwin build-darwin-arm64 build-windows
	@echo ""
	@echo "✅ All platforms built successfully!"
	@ls -lh ${BINARY_NAME}-*

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Run tests with race detector
race:
	@echo "Running tests with race detector..."
	go test -race -v ./...

# Generate coverage report
coverage:
	@echo "Generating coverage report..."
	go test -v -coverprofile=coverage.out ./...
	@echo ""
	@echo "Coverage summary:"
	go tool cover -func=coverage.out | grep total
	@echo ""
	@echo "To view HTML report: go tool cover -html=coverage.out"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -f ${BINARY_NAME}
	rm -f ${BINARY_NAME}-*
	rm -f ${WS_TEST_NAME}
	rm -f coverage.out
	rm -rf dist/
	@echo "✅ Clean complete"

# Install to /usr/local/bin
install: build
	@echo "Installing to /usr/local/bin..."
	sudo cp ${BINARY_NAME} /usr/local/bin/
	sudo chmod +x /usr/local/bin/${BINARY_NAME}
	@echo "✅ Installed: /usr/local/bin/${BINARY_NAME}"
	@which ${BINARY_NAME}

# Uninstall from /usr/local/bin
uninstall:
	@echo "Uninstalling from /usr/local/bin..."
	sudo rm -f /usr/local/bin/${BINARY_NAME}
	@echo "✅ Uninstalled"

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...
	@echo "✅ Code formatted"

# Run linter
lint:
	@echo "Running linter..."
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "⚠️  golangci-lint not installed. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Run go vet
vet:
	@echo "Running go vet..."
	go vet ./...
	@echo "✅ go vet complete"

# Update dependencies
deps:
	@echo "Updating dependencies..."
	go get -u ./...
	go mod tidy
	@echo "✅ Dependencies updated"

# Show version info
version:
	@echo "openpal"
	@echo "Version: ${VERSION}"
	@echo "Go: ${GO_VERSION}"
	@echo "Commit: ${GIT_COMMIT}"
	@echo "Build Time: ${BUILD_TIME}"

# Run openpal (requires arguments)
run:
	@echo "Running openpal..."
	@echo "Usage: make run ARGS='--provider claude --quest-id q1 --task \"Refactor code\"'"
	@echo ""
	./${BINARY_NAME} ${ARGS}

# Create release archive
release: build-all
	@echo "Creating release archive..."
	mkdir -p dist
	tar -czvf dist/${BINARY_NAME}-${VERSION}-linux-amd64.tar.gz ${BINARY_NAME}-linux-amd64
	tar -czvf dist/${BINARY_NAME}-${VERSION}-linux-arm64.tar.gz ${BINARY_NAME}-linux-arm64
	tar -czvf dist/${BINARY_NAME}-${VERSION}-darwin-amd64.tar.gz ${BINARY_NAME}-darwin-amd64
	tar -czvf dist/${BINARY_NAME}-${VERSION}-darwin-arm64.tar.gz ${BINARY_NAME}-darwin-arm64
	zip -j dist/${BINARY_NAME}-${VERSION}-windows-amd64.zip ${BINARY_NAME}-windows-amd64.exe
	@echo ""
	@echo "✅ Release archives created in dist/"
	@ls -lh dist/

# Check for common issues
check: fmt vet lint
	@echo ""
	@echo "✅ All checks passed!"

# Full CI pipeline
all: check build test coverage
	@echo ""
	@echo "✅ CI pipeline complete!"
	@echo ""
	@echo "Build: ✅"
	@echo "Tests: ✅"
	@echo "Coverage: Generated (coverage.out)"
	@echo ""

# Development mode (watch and rebuild)
dev:
	@echo "Starting development mode..."
	@if command -v air > /dev/null; then \
		air; \
	else \
		echo "⚠️  air not installed. Install with: go install github.com/air-verse/air@latest"; \
		echo "   Or use: go run ./cmd/openpal/main.go ..."; \
	fi

# Docker build (if Dockerfile exists)
docker-build:
	@echo "Building Docker image..."
	docker build -t ${BINARY_NAME}:${VERSION} .
	@echo "✅ Docker image built: ${BINARY_NAME}:${VERSION}"

# Docker run
docker-run:
	@echo "Running Docker container..."
	docker run --rm -it ${BINARY_NAME}:${VERSION} ${ARGS}

# Help for Tavern integration
tavern-help:
	@echo "Tavern Integration Commands:"
	@echo ""
	@echo "1. Build and install on remote server:"
	@echo "   make build-linux"
	@echo "   scp ${BINARY_NAME}-linux-amd64 user@server:/usr/local/bin/openpal"
	@echo "   ssh user@server 'chmod +x /usr/local/bin/openpal'"
	@echo ""
	@echo "2. Run in screen session:"
	@echo "   ssh user@server 'screen -dmS openpal-q1 openpal run --provider claude --quest-id q1 --task \"Refactor code\"'"
	@echo ""
	@echo "3. Check status:"
	@echo "   ssh user@server 'cat /tmp/openpal/q1/status.json | jq .'"
	@echo "   ssh user@server 'cat /tmp/openpal/q1/progress.json | jq .'"
	@echo ""
	@echo "4. View logs:"
	@echo "   ssh user@server 'tail -f /tmp/openpal/q1/output.jsonl'"
	@echo ""
	@echo "5. Test WebSocket connection:"
	@echo "   make build-ws-test"
	@echo "   ./ws-test -url ws://localhost:8765/ws -quest-id q1 -i"
	@echo ""

# Build and run WebSocket test
ws-test: build-ws-test
	@echo "Running WebSocket test..."
	./${WS_TEST_NAME} ${ARGS}

