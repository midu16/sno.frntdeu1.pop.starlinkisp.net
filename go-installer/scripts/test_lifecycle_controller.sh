#!/bin/bash

# Lifecycle Controller Test Script
# This script tests the iDRAC lifecycle controller information functionality

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
IDRAC_IP="${IDRAC_IP:-192.168.1.228}"
IDRAC_USER="${IDRAC_USER:-root}"
IDRAC_PASSWORD="${IDRAC_PASSWORD:-}"
VERIFY_SSL="${VERIFY_SSL:-false}"

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

# Get iDRAC password
get_idrac_password() {
    if [ -z "$IDRAC_PASSWORD" ]; then
        read -s -p "Enter iDRAC password: " IDRAC_PASSWORD
        echo
    fi
    
    if [ -z "$IDRAC_PASSWORD" ]; then
        log_error "iDRAC password is required"
        exit 1
    fi
}

# Make iDRAC API call
idrac_api_call() {
    local method="$1"
    local endpoint="$2"
    local data="${3:-}"
    
    local curl_opts=(
        -s
        -X "$method"
        -H "Content-Type: application/json"
        -u "$IDRAC_USER:$IDRAC_PASSWORD"
    )
    
    if [ "$VERIFY_SSL" = "false" ]; then
        curl_opts+=(-k)
    fi
    
    if [ -n "$data" ]; then
        curl_opts+=(-d "$data")
    fi
    
    curl "${curl_opts[@]}" "https://$IDRAC_IP$endpoint"
}

# Test lifecycle controller info endpoint
test_lifecycle_controller_info() {
    log_info "Testing lifecycle controller information endpoint..."
    
    local response
    response=$(idrac_api_call "GET" "/redfish/v1/Managers/iDRAC.Embedded.1")
    
    if echo "$response" | jq -e '.FirmwareVersion' > /dev/null 2>&1; then
        local firmware_version
        local id
        local name
        local health
        local state
        
        firmware_version=$(echo "$response" | jq -r '.FirmwareVersion // "Unknown"')
        id=$(echo "$response" | jq -r '.Id // "Unknown"')
        name=$(echo "$response" | jq -r '.Name // "Unknown"')
        health=$(echo "$response" | jq -r '.Status.Health // "Unknown"')
        state=$(echo "$response" | jq -r '.Status.State // "Unknown"')
        
        log_success "Lifecycle controller information retrieved successfully"
        log_info "  Firmware Version: $firmware_version"
        log_info "  ID: $id"
        log_info "  Name: $name"
        log_info "  Health: $health"
        log_info "  State: $state"
        return 0
    else
        log_error "Failed to retrieve lifecycle controller information"
        return 1
    fi
}

# Test system info endpoint (for comparison)
test_system_info() {
    log_info "Testing system information endpoint..."
    
    local response
    response=$(idrac_api_call "GET" "/redfish/v1/Systems/System.Embedded.1")
    
    if echo "$response" | jq -e '.Manufacturer' > /dev/null 2>&1; then
        local manufacturer
        local model
        local serial
        local bios
        local power_state
        
        manufacturer=$(echo "$response" | jq -r '.Manufacturer')
        model=$(echo "$response" | jq -r '.Model')
        serial=$(echo "$response" | jq -r '.SerialNumber')
        bios=$(echo "$response" | jq -r '.BiosVersion')
        power_state=$(echo "$response" | jq -r '.PowerState')
        
        log_success "System information retrieved successfully"
        log_info "  Manufacturer: $manufacturer"
        log_info "  Model: $model"
        log_info "  Serial Number: $serial"
        log_info "  BIOS Version: $bios"
        log_info "  Power State: $power_state"
        return 0
    else
        log_error "Failed to retrieve system information"
        return 1
    fi
}

# Test Go application lifecycle controller command
test_go_lifecycle_controller() {
    log_info "Testing Go application lifecycle controller command..."
    
    local binary="openshift-sno-hub-installer"
    
    if [ ! -f "$binary" ]; then
        log_error "Binary not found: $binary"
        return 1
    fi
    
    # Test lifecycle controller command
    if "$binary" lifecycle-controller 2>&1 | grep -q "iDRAC Lifecycle Controller Information"; then
        log_success "Go application lifecycle controller command works correctly"
        return 0
    else
        log_error "Go application lifecycle controller command failed"
        return 1
    fi
}

# Test Go application info command
test_go_info() {
    log_info "Testing Go application info command..."
    
    local binary="openshift-sno-hub-installer"
    
    if [ ! -f "$binary" ]; then
        log_error "Binary not found: $binary"
        return 1
    fi
    
    # Test info command (should now include lifecycle controller info)
    if "$binary" info 2>&1 | grep -q "iDRAC Lifecycle Controller Information"; then
        log_success "Go application info command includes lifecycle controller information"
        return 0
    else
        log_warn "Go application info command may not include lifecycle controller information"
        return 1
    fi
}

# Run all tests
run_all_tests() {
    local failed_tests=0
    
    log_info "Starting Lifecycle Controller Tests..."
    log_info "Target iDRAC: $IDRAC_IP"
    log_info "=========================================="
    
    # Test lifecycle controller info
    if ! test_lifecycle_controller_info; then
        ((failed_tests++))
    fi
    
    # Test system info for comparison
    if ! test_system_info; then
        ((failed_tests++))
    fi
    
    # Test Go application lifecycle controller command
    if ! test_go_lifecycle_controller; then
        ((failed_tests++))
    fi
    
    # Test Go application info command
    if ! test_go_info; then
        ((failed_tests++))
    fi
    
    log_info "=========================================="
    
    if [ $failed_tests -eq 0 ]; then
        log_success "All Lifecycle Controller tests passed!"
        return 0
    else
        log_error "$failed_tests test(s) failed"
        return 1
    fi
}

# Main function
main() {
    log_info "Lifecycle Controller Test Script"
    log_info "================================"
    
    get_idrac_password
    run_all_tests
}

# Run main function
main "$@"
