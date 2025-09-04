#!/bin/bash

# Go Application Test Script
# This script tests the Go application functionality

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

# Check if Go is installed
check_go() {
    log_info "Checking Go installation..."
    
    if ! command -v go &> /dev/null; then
        log_error "Go is not installed or not in PATH"
        exit 1
    fi
    
    local go_version
    go_version=$(go version)
    log_success "Go is installed: $go_version"
}

# Check if required Go tools are available
check_go_tools() {
    log_info "Checking Go tools..."
    
    local missing_tools=()
    
    if ! command -v gofmt &> /dev/null; then
        missing_tools+=("gofmt")
    fi
    
    if ! command -v go vet &> /dev/null; then
        missing_tools+=("go vet")
    fi
    
    if [ ${#missing_tools[@]} -ne 0 ]; then
        log_warn "Some Go tools are missing: ${missing_tools[*]}"
        log_info "This is normal if Go is not fully installed"
    else
        log_success "Go tools are available"
    fi
}

# Download dependencies
download_dependencies() {
    log_info "Downloading Go dependencies..."
    
    if go mod tidy; then
        log_success "Dependencies downloaded successfully"
    else
        log_error "Failed to download dependencies"
        exit 1
    fi
}

# Format code
format_code() {
    log_info "Formatting Go code..."
    
    if gofmt -s -w .; then
        log_success "Code formatted successfully"
    else
        log_error "Failed to format code"
        exit 1
    fi
}

# Vet code
vet_code() {
    log_info "Vetting Go code..."
    
    if go vet ./...; then
        log_success "Code vetting passed"
    else
        log_error "Code vetting failed"
        exit 1
    fi
}

# Run tests
run_tests() {
    log_info "Running Go tests..."
    
    if go test -v ./...; then
        log_success "All tests passed"
    else
        log_error "Some tests failed"
        exit 1
    fi
}

# Build the application
build_application() {
    log_info "Building the application..."
    
    local build_dir="build"
    mkdir -p "$build_dir"
    
    if go build -o "$build_dir/openshift-sno-hub-installer" cmd/openshift-sno-hub-installer/main.go; then
        log_success "Application built successfully"
        log_info "Binary location: $build_dir/openshift-sno-hub-installer"
    else
        log_error "Failed to build application"
        exit 1
    fi
}

# Test application help
test_help() {
    log_info "Testing application help..."
    
    local binary="build/openshift-sno-hub-installer"
    
    if [ ! -f "$binary" ]; then
        log_error "Binary not found: $binary"
        return 1
    fi
    
    if "$binary" help 2>&1 | grep -q "Usage:"; then
        log_success "Help command works correctly"
    else
        log_error "Help command failed"
        return 1
    fi
}

# Test configuration creation
test_config() {
    log_info "Testing configuration creation..."
    
    local binary="build/openshift-sno-hub-installer"
    local config_file="idrac_config.yaml"
    
    if [ ! -f "$binary" ]; then
        log_error "Binary not found: $binary"
        return 1
    fi
    
    # Remove existing config file if it exists
    rm -f "$config_file"
    
    if "$binary" config; then
        if [ -f "$config_file" ]; then
            log_success "Configuration file created successfully"
            log_info "Configuration file: $config_file"
        else
            log_error "Configuration file was not created"
            return 1
        fi
    else
        log_error "Configuration creation failed"
        return 1
    fi
}

# Test application with invalid command
test_invalid_command() {
    log_info "Testing invalid command handling..."
    
    local binary="build/openshift-sno-hub-installer"
    
    if [ ! -f "$binary" ]; then
        log_error "Binary not found: $binary"
        return 1
    fi
    
    if "$binary" invalid-command 2>&1 | grep -q "Usage:"; then
        log_success "Invalid command handling works correctly"
    else
        log_error "Invalid command handling failed"
        return 1
    fi
}

# Run all tests
run_all_tests() {
    local failed_tests=0
    
    log_info "Starting Go application tests..."
    log_info "================================="
    
    # Check Go installation
    if ! check_go; then
        ((failed_tests++))
    fi
    
    # Check Go tools
    check_go_tools
    
    # Download dependencies
    if ! download_dependencies; then
        ((failed_tests++))
    fi
    
    # Format code
    if ! format_code; then
        ((failed_tests++))
    fi
    
    # Vet code
    if ! vet_code; then
        ((failed_tests++))
    fi
    
    # Run tests
    if ! run_tests; then
        ((failed_tests++))
    fi
    
    # Build application
    if ! build_application; then
        ((failed_tests++))
    fi
    
    # Test help
    if ! test_help; then
        ((failed_tests++))
    fi
    
    # Test config
    if ! test_config; then
        ((failed_tests++))
    fi
    
    # Test invalid command
    if ! test_invalid_command; then
        ((failed_tests++))
    fi
    
    log_info "================================="
    
    if [ $failed_tests -eq 0 ]; then
        log_success "All Go application tests passed!"
        return 0
    else
        log_error "$failed_tests test(s) failed"
        return 1
    fi
}

# Cleanup function
cleanup() {
    log_info "Cleaning up test artifacts..."
    
    # Remove build directory
    if [ -d "build" ]; then
        rm -rf build
        log_info "Removed build directory"
    fi
    
    # Remove config file
    if [ -f "idrac_config.yaml" ]; then
        rm -f idrac_config.yaml
        log_info "Removed test config file"
    fi
    
    # Remove logs directory
    if [ -d "logs" ]; then
        rm -rf logs
        log_info "Removed logs directory"
    fi
}

# Main function
main() {
    log_info "Go Application Test Script"
    log_info "=========================="
    
    # Set up cleanup trap
    trap cleanup EXIT
    
    run_all_tests
}

# Run main function
main "$@"