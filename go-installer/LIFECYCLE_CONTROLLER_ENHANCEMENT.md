# Lifecycle Controller Enhancement

## Overview

The `info` command has been enhanced to include iDRAC lifecycle controller version information. This provides comprehensive system information including both hardware details and iDRAC firmware information.

## Enhancement Details

### New Functionality

1. **Enhanced `info` Command**: Now displays both system information and lifecycle controller information
2. **New `lifecycle-controller` Command**: Dedicated command to retrieve only lifecycle controller information
3. **Comprehensive System Information**: Complete view of system and iDRAC status

### Information Retrieved

The enhanced `info` command now provides:

#### System Information
- Manufacturer
- Model
- Serial Number
- BIOS Version
- Power State
- Health Status

#### iDRAC Lifecycle Controller Information
- Firmware Version
- Controller ID
- Controller Name
- Health Status
- State

## Usage Examples

### Enhanced Info Command

```bash
# Get comprehensive system and lifecycle controller information
./openshift-sno-hub-installer info
```

**Output Example**:
```
[INFO] Getting system information...
[INFO] System Information:
[INFO]   Manufacturer: Dell Inc.
[INFO]   Model: PowerEdge R640
[INFO]   Serial Number: ABC123456
[INFO]   BIOS Version: 2.15.0
[INFO] Getting iDRAC lifecycle controller information...
[INFO] iDRAC Lifecycle Controller Information:
[INFO]   Firmware Version: 4.40.00.00
[INFO]   ID: iDRAC.Embedded.1
[INFO]   Name: iDRAC
[INFO]   Health: OK
[INFO]   State: Enabled
```

### Dedicated Lifecycle Controller Command

```bash
# Get only lifecycle controller information
./openshift-sno-hub-installer lifecycle-controller
```

**Output Example**:
```
[INFO] Getting iDRAC lifecycle controller information...
[INFO] iDRAC Lifecycle Controller Information:
[INFO]   Firmware Version: 4.40.00.00
[INFO]   ID: iDRAC.Embedded.1
[INFO]   Name: iDRAC
[INFO]   Health: OK
[INFO]   State: Enabled
```

## Implementation Details

### New Data Structure

```go
type LifecycleControllerInfo struct {
    FirmwareVersion string `json:"FirmwareVersion"`
    Id              string `json:"Id"`
    Name            string `json:"Name"`
    Status          struct {
        Health string `json:"Health"`
        State  string `json:"State"`
    } `json:"Status"`
}
```

### API Endpoint

The enhancement uses the iDRAC Redfish API endpoint:
- **Endpoint**: `/redfish/v1/Managers/iDRAC.Embedded.1`
- **Method**: GET
- **Authentication**: Basic authentication with iDRAC credentials

### Error Handling

- **Graceful Degradation**: If lifecycle controller information cannot be retrieved, the system information is still displayed
- **Warning Messages**: Clear warning messages when lifecycle controller information is unavailable
- **Comprehensive Logging**: Detailed logging for troubleshooting

## Files Modified/Created

### New Files
- `internal/idrac/lifecycle_controller.go` - Lifecycle controller functionality
- `scripts/test_lifecycle_controller.sh` - Test script for lifecycle controller functionality
- `LIFECYCLE_CONTROLLER_ENHANCEMENT.md` - This documentation

### Modified Files
- `internal/idrac/client_enhanced.go` - Added lifecycle controller method to enhanced client
- `internal/app/app.go` - Enhanced info command and added lifecycle-controller command
- `README.md` - Updated with new command information

## Testing and Validation

### Test Script

A comprehensive test script is provided:

```bash
./scripts/test_lifecycle_controller.sh
```

**Test Coverage**:
- Lifecycle controller information retrieval
- System information retrieval (for comparison)
- Go application lifecycle controller command
- Go application enhanced info command

### Manual Testing

```bash
# Test enhanced info command
./openshift-sno-hub-installer info

# Test dedicated lifecycle controller command
./openshift-sno-hub-installer lifecycle-controller

# Test help command (should show new command)
./openshift-sno-hub-installer help
```

## Benefits

### 1. Comprehensive System Information
- **Complete View**: Both hardware and iDRAC information in one command
- **Firmware Version**: Easy access to iDRAC firmware version
- **Health Status**: Both system and iDRAC health information

### 2. Enhanced Troubleshooting
- **Firmware Information**: Quick access to iDRAC firmware version for troubleshooting
- **Controller Status**: iDRAC controller health and state information
- **Comprehensive Logging**: Detailed information for debugging

### 3. Better User Experience
- **Single Command**: Get all system information with one command
- **Dedicated Command**: Option to get only lifecycle controller information
- **Clear Output**: Well-formatted, easy-to-read information

### 4. iDRAC Management
- **Version Tracking**: Easy tracking of iDRAC firmware versions
- **Health Monitoring**: Monitor iDRAC controller health
- **Status Verification**: Verify iDRAC controller state

## Compatibility

### iDRAC Support
- **iDRAC 8**: Full support for iDRAC 8 systems
- **iDRAC 9**: Compatible with iDRAC 9 systems
- **Legacy Support**: Works with older iDRAC versions

### API Compatibility
- **Redfish API**: Uses standard Redfish API endpoints
- **Backward Compatibility**: Works with various iDRAC firmware versions
- **Error Handling**: Graceful handling of unsupported features

## Configuration

### iDRAC Configuration

Ensure your iDRAC configuration includes proper credentials:

```yaml
idrac:
  ip: "192.168.1.228"
  username: "root"
  password: "your-password"
  verify_ssl: false
  timeout: 30
```

### Requirements

- iDRAC with Redfish API support
- Network access to iDRAC
- Proper iDRAC user permissions
- Valid iDRAC credentials

## Troubleshooting

### Common Issues

1. **Lifecycle Controller Information Not Available**
   - Check iDRAC firmware version
   - Verify Redfish API support
   - Check iDRAC user permissions

2. **Connection Issues**
   - Verify iDRAC IP address
   - Check network connectivity
   - Verify iDRAC credentials

3. **API Errors**
   - Check iDRAC firmware version
   - Verify Redfish API endpoint availability
   - Check iDRAC configuration

### Debug Information

Enable debug logging to get detailed information:

```bash
# Check system and lifecycle controller information
./openshift-sno-hub-installer info

# Check only lifecycle controller information
./openshift-sno-hub-installer lifecycle-controller
```

## Future Enhancements

- **Firmware Update Information**: Display available firmware updates
- **Configuration Backup**: Backup iDRAC configuration
- **Health Monitoring**: Continuous health monitoring
- **Alert Management**: iDRAC alert management
- **Performance Metrics**: iDRAC performance information

This enhancement provides comprehensive system and iDRAC information, making it easier to manage and troubleshoot OpenShift SNO hub installations with iDRAC systems.
