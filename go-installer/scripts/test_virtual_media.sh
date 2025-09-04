#!/bin/bash

# Enhanced Virtual Media Test Script
# This script tests the enhanced virtual media functionality

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

# Test virtual media info endpoint
test_virtual_media_info() {
    log_info "Testing virtual media information endpoint..."
    
    local response
    response=$(idrac_api_call "GET" "/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD")
    
    if echo "$response" | jq -e '.Inserted' > /dev/null 2>&1; then
        local inserted
        local image
        local image_name
        
        inserted=$(echo "$response" | jq -r '.Inserted')
        image=$(echo "$response" | jq -r '.Image // "None"')
        image_name=$(echo "$response" | jq -r '.ImageName // "None"')
        
        log_success "Virtual media information retrieved successfully"
        log_info "  Inserted: $inserted"
        log_info "  Image: $image"
        log_info "  Image Name: $image_name"
        return 0
    else
        log_error "Failed to retrieve virtual media information"
        return 1
    fi
}

# Test enhanced virtual CD boot configuration
test_enhanced_virtual_cd_boot() {
    log_info "Testing enhanced virtual CD boot configuration..."
    
    # Test RemoteCd (most common for iDRAC 8)
    local remote_cd_data='{"Boot":{"BootSourceOverrideTarget":"RemoteCd","BootSourceOverrideEnabled":"Once"}}'
    local response
    response=$(idrac_api_call "PATCH" "/redfish/v1/Systems/System.Embedded.1" "$remote_cd_data")
    
    if [ $? -eq 0 ]; then
        log_success "Enhanced virtual CD boot configuration (RemoteCd) successful"
        return 0
    else
        log_warn "RemoteCd boot configuration failed, trying VirtualCd..."
        
        # Test VirtualCd
        local virtual_cd_data='{"Boot":{"BootSourceOverrideTarget":"VirtualCd","BootSourceOverrideEnabled":"Once"}}'
        response=$(idrac_api_call "PATCH" "/redfish/v1/Systems/System.Embedded.1" "$virtual_cd_data")
        
        if [ $? -eq 0 ]; then
            log_success "Enhanced virtual CD boot configuration (VirtualCd) successful"
            return 0
        else
            log_warn "VirtualCd boot configuration failed, trying Cd..."
            
            # Test Cd (fallback)
            local cd_data='{"Boot":{"BootSourceOverrideTarget":"Cd","BootSourceOverrideEnabled":"Once"}}'
            response=$(idrac_api_call "PATCH" "/redfish/v1/Systems/System.Embedded.1" "$cd_data")
            
            if [ $? -eq 0 ]; then
                log_success "Enhanced virtual CD boot configuration (Cd) successful"
                return 0
            else
                log_error "All virtual CD boot configuration attempts failed"
                return 1
            fi
        fi
    fi
}

# Test virtual media eject
test_virtual_media_eject() {
    log_info "Testing virtual media eject..."
    
    local response
    response=$(idrac_api_call "POST" "/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.EjectMedia" '{}')
    
    if [ $? -eq 0 ]; then
        log_success "Virtual media eject successful"
        return 0
    else
        log_error "Failed to eject virtual media"
        return 1
    fi
}

# Test virtual media insert
test_virtual_media_insert() {
    log_info "Testing virtual media insert..."
    
    local test_iso_url="http://example.com/test.iso"
    local data="{\"Image\": \"$test_iso_url\"}"
    local response
    response=$(idrac_api_call "POST" "/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.InsertMedia" "$data")
    
    if [ $? -eq 0 ]; then
        log_success "Virtual media insert successful"
        return 0
    else
        log_error "Failed to insert virtual media"
        return 1
    fi
}

# Test complete virtual media boot process
test_complete_virtual_media_process() {
    log_info "Testing complete virtual media boot process..."
    
    local test_iso_url="http://example.com/test.iso"
    
    # Step 1: Eject existing media
    log_info "Step 1: Ejecting existing virtual media..."
    if ! test_virtual_media_eject; then
        log_warn "Failed to eject existing media, continuing..."
    fi
    
    # Step 2: Insert new media
    log_info "Step 2: Inserting new virtual media..."
    if ! test_virtual_media_insert; then
        log_error "Failed to insert virtual media"
        return 1
    fi
    
    # Step 3: Set boot to virtual CD
    log_info "Step 3: Setting boot to virtual CD..."
    if ! test_enhanced_virtual_cd_boot; then
        log_error "Failed to set boot to virtual CD"
        return 1
    fi
    
    log_success "Complete virtual media boot process test successful"
    return 0
}

# Run all tests
run_all_tests() {
    local failed_tests=0
    
    log_info "Starting Enhanced Virtual Media Tests..."
    log_info "Target iDRAC: $IDRAC_IP"
    log_info "=========================================="
    
    # Test virtual media info
    if ! test_virtual_media_info; then
        ((failed_tests++))
    fi
    
    # Test enhanced virtual CD boot
    if ! test_enhanced_virtual_cd_boot; then
        ((failed_tests++))
    fi
    
    # Test virtual media eject
    if ! test_virtual_media_eject; then
        ((failed_tests++))
    fi
    
    # Test virtual media insert
    if ! test_virtual_media_insert; then
        ((failed_tests++))
    fi
    
    # Test complete process
    if ! test_complete_virtual_media_process; then
        ((failed_tests++))
    fi
    
    log_info "=========================================="
    
    if [ $failed_tests -eq 0 ]; then
        log_success "All Enhanced Virtual Media tests passed!"
        return 0
    else
        log_error "$failed_tests test(s) failed"
        return 1
    fi
}

# Main function
main() {
    log_info "Enhanced Virtual Media Test Script"
    log_info "=================================="
    
    get_idrac_password
    run_all_tests
}

# Run main function
main "$@"
