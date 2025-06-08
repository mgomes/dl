# Makefile for dl - Fast download manager

# Variables
BINARY_NAME=dl
GO=go
GO_BUILD=$(GO) build
GO_TEST=$(GO) test
GO_CLEAN=$(GO) clean
GO_LINT=golangci-lint
GO_MOD=$(GO) mod
GO_FILES=$(wildcard *.go)
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

# Build variables
GOOS?=$(shell go env GOOS)
GOARCH?=$(shell go env GOARCH)

# Default target
.PHONY: all
all: help

# Build the application
.PHONY: build
build:
	@echo "Building $(BINARY_NAME)..."
	@$(GO_BUILD) $(LDFLAGS) -o $(BINARY_NAME) .

# Build for all platforms
.PHONY: build-all
build-all: build-linux build-darwin build-windows

# Build for Linux
.PHONY: build-linux
build-linux:
	@echo "Building for Linux..."
	@GOOS=linux GOARCH=amd64 $(GO_BUILD) $(LDFLAGS) -o $(BINARY_NAME)-linux-amd64 .
	@GOOS=linux GOARCH=arm64 $(GO_BUILD) $(LDFLAGS) -o $(BINARY_NAME)-linux-arm64 .

# Build for macOS
.PHONY: build-darwin
build-darwin:
	@echo "Building for macOS..."
	@GOOS=darwin GOARCH=amd64 $(GO_BUILD) $(LDFLAGS) -o $(BINARY_NAME)-darwin-amd64 .
	@GOOS=darwin GOARCH=arm64 $(GO_BUILD) $(LDFLAGS) -o $(BINARY_NAME)-darwin-arm64 .

# Build for Windows
.PHONY: build-windows
build-windows:
	@echo "Building for Windows..."
	@GOOS=windows GOARCH=amd64 $(GO_BUILD) $(LDFLAGS) -o $(BINARY_NAME)-windows-amd64.exe .

# Run tests
.PHONY: test
test:
	@echo "Running tests..."
	@$(GO_TEST) -v ./...

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	@echo "Running tests with coverage..."
	@$(GO_TEST) -v -coverprofile=coverage.out ./...
	@$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Run linter
.PHONY: lint
lint:
	@if command -v $(GO_LINT) >/dev/null 2>&1; then \
		echo "Running linter..."; \
		$(GO_LINT) run; \
	else \
		echo "golangci-lint not installed. Install with:"; \
		echo "  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin"; \
	fi

# Format code
.PHONY: fmt
fmt:
	@echo "Formatting code..."
	@$(GO) fmt ./...

# Update dependencies
.PHONY: deps
deps:
	@echo "Updating dependencies..."
	@$(GO_MOD) download
	@$(GO_MOD) tidy

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning..."
	@$(GO_CLEAN)
	@rm -f $(BINARY_NAME) $(BINARY_NAME)-*
	@rm -f coverage.out coverage.html
	@rm -f *.dl_progress

# Install locally
.PHONY: install
install: build
	@echo "Installing $(BINARY_NAME)..."
	@cp $(BINARY_NAME) $(GOPATH)/bin/$(BINARY_NAME) || cp $(BINARY_NAME) ~/go/bin/$(BINARY_NAME)

# Uninstall
.PHONY: uninstall
uninstall:
	@echo "Uninstalling $(BINARY_NAME)..."
	@rm -f $(GOPATH)/bin/$(BINARY_NAME) ~/go/bin/$(BINARY_NAME)

# Run a test download
.PHONY: test-download
test-download: build
	@echo "Testing download with 10MB file..."
	@./$(BINARY_NAME) -boost 8 http://speedtest.tele2.net/10MB.zip

# Run a test download with resume
.PHONY: test-resume
test-resume: build
	@echo "Testing resume functionality..."
	@if [ -f test_resume.sh ]; then \
		./test_resume.sh; \
	else \
		echo "test_resume.sh not found"; \
	fi

# Development run with race detector
.PHONY: dev
dev:
	@echo "Building with race detector..."
	@$(GO_BUILD) -race $(LDFLAGS) -o $(BINARY_NAME) .
	@echo "Ready for development testing"

# Show help
.PHONY: help
help:
	@echo "dl - Fast download manager"
	@echo ""
	@echo "Usage:"
	@echo "  make build          - Build the application"
	@echo "  make build-all      - Build for all platforms"
	@echo "  make test           - Run tests"
	@echo "  make test-coverage  - Run tests with coverage report"
	@echo "  make lint           - Run linter"
	@echo "  make fmt            - Format code"
	@echo "  make deps           - Update dependencies"
	@echo "  make clean          - Clean build artifacts"
	@echo "  make install        - Install locally"
	@echo "  make uninstall      - Uninstall from local system"
	@echo "  make test-download  - Test download functionality"
	@echo "  make test-resume    - Test resume functionality"
	@echo "  make dev            - Build with race detector for development"
	@echo "  make help           - Show this help message"