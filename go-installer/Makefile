# OpenShift SNO Hub Installer Makefile

.PHONY: build clean test install deps lint fmt vet run help

# Variables
BINARY_NAME=openshift-sno-hub-installer
BUILD_DIR=build
VERSION=$(shell git describe --tags --always --dirty)
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

# Default target
all: deps build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) cmd/openshift-sno-hub-installer/main.go
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
	rm -rf logs
	@echo "Clean complete"

# Install dependencies
deps:
	@echo "Installing dependencies..."
	go mod tidy
	go mod download
	@echo "Dependencies installed"

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Lint the code
lint:
	@echo "Running linter..."
	golangci-lint run

# Format the code
fmt:
	@echo "Formatting code..."
	go fmt ./...

# Vet the code
vet:
	@echo "Vetting code..."
	go vet ./...

# Run the application
run: build
	@echo "Running $(BINARY_NAME)..."
	./$(BUILD_DIR)/$(BINARY_NAME)

# Run with specific command
run-config: build
	./$(BUILD_DIR)/$(BINARY_NAME) config

run-install: build
	./$(BUILD_DIR)/$(BINARY_NAME) install

# Create release build
release: clean deps
	@echo "Creating release build..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 cmd/openshift-sno-hub-installer/main.go
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 cmd/openshift-sno-hub-installer/main.go
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe cmd/openshift-sno-hub-installer/main.go
	@echo "Release builds complete"

# Install the binary to GOPATH/bin
install: build
	@echo "Installing $(BINARY_NAME)..."
	go install $(LDFLAGS) cmd/openshift-sno-hub-installer/main.go
	@echo "Installation complete"

# Create Docker image
docker-build:
	@echo "Building Docker image..."
	docker build -t $(BINARY_NAME):$(VERSION) .
	docker tag $(BINARY_NAME):$(VERSION) $(BINARY_NAME):latest
	@echo "Docker image built: $(BINARY_NAME):$(VERSION)"

# Run in Docker
docker-run: docker-build
	@echo "Running in Docker..."
	docker run --rm -it $(BINARY_NAME):latest

# Generate documentation
docs:
	@echo "Generating documentation..."
	godoc -http=:6060 &
	@echo "Documentation server started at http://localhost:6060"

# Check for security vulnerabilities
security:
	@echo "Checking for security vulnerabilities..."
	gosec ./...

# Benchmark tests
benchmark:
	@echo "Running benchmarks..."
	go test -bench=. ./...

# Show help
help:
	@echo "Available targets:"
	@echo "  build         - Build the binary"
	@echo "  clean         - Clean build artifacts"
	@echo "  deps          - Install dependencies"
	@echo "  test          - Run tests"
	@echo "  test-coverage - Run tests with coverage"
	@echo "  lint          - Run linter"
	@echo "  fmt           - Format code"
	@echo "  vet           - Vet code"
	@echo "  run           - Run the application"
	@echo "  run-config    - Run with config command"
	@echo "  run-install   - Run with install command"
	@echo "  release       - Create release builds"
	@echo "  install       - Install to GOPATH/bin"
	@echo "  docker-build  - Build Docker image"
	@echo "  docker-run    - Run in Docker"
	@echo "  docs          - Generate documentation"
	@echo "  security      - Check for security vulnerabilities"
	@echo "  benchmark     - Run benchmarks"
	@echo "  help          - Show this help"