# Enhanced Virtual Media Support for iDRAC 8

## Overview

The enhanced virtual media support addresses the issue where the original `set-boot-cd` command was configuring the local physical CD/DVD drive instead of the iDRAC 8 virtual CD/DVD/ISO. This enhancement provides proper virtual media management for iDRAC 8 systems.

## Problem Solved

**Original Issue**: The `set-boot-cd` command was using `"Cd"` as the boot source override target, which refers to the physical CD/DVD drive, not the virtual media mounted via iDRAC.

**Solution**: Enhanced implementation that tries multiple virtual CD boot options in order of compatibility:
1. `"RemoteCd"` - Most common for iDRAC 8 virtual media
2. `"VirtualCd"` - Alternative virtual CD option
3. `"Cd"` - Fallback to physical CD (for compatibility)

## New Commands

### Enhanced Boot Commands

#### `set-boot-cd-enhanced`
Sets the boot device to Virtual CD/DVD with enhanced compatibility for iDRAC 8.

```bash
./openshift-sno-hub-installer set-boot-cd-enhanced
```

**Features**:
- Tries multiple virtual CD boot options automatically
- Provides detailed logging of each attempt
- Falls back gracefully if one option fails
- Checks virtual media status before setting boot

#### `virtual-media-info`
Retrieves detailed information about the current virtual media status.

```bash
./openshift-sno-hub-installer virtual-media-info
```

**Information Provided**:
- Connection method (ConnectedVia)
- Current image URL
- Image name
- Insertion status
- Supported media types

#### `manage-virtual-boot`
Manages the complete virtual media boot process with enhanced error handling.

```bash
./openshift-sno-hub-installer manage-virtual-boot <ISO_URL>
```

**Process Steps**:
1. Eject any existing virtual media
2. Insert the new ISO image
3. Set boot device to virtual CD/DVD (enhanced)
4. Restart the system

## Enhanced Implementation Details

### Virtual Media Boot Options

The enhanced implementation tries the following boot options in order:

1. **RemoteCd** - Primary option for iDRAC 8
   ```json
   {
     "Boot": {
       "BootSourceOverrideTarget": "RemoteCd",
       "BootSourceOverrideEnabled": "Once"
     }
   }
   ```

2. **VirtualCd** - Alternative virtual CD option
   ```json
   {
     "Boot": {
       "BootSourceOverrideTarget": "VirtualCd",
       "BootSourceOverrideEnabled": "Once"
     }
   }
   ```

3. **Cd** - Fallback to physical CD
   ```json
   {
     "Boot": {
       "BootSourceOverrideTarget": "Cd",
       "BootSourceOverrideEnabled": "Once"
     }
   }
   ```

### Error Handling

The enhanced implementation includes comprehensive error handling:

- **Graceful Fallback**: If one boot option fails, it automatically tries the next
- **Detailed Logging**: Each attempt is logged with success/failure status
- **Status Verification**: Checks virtual media status before and after operations
- **Timeout Handling**: Proper timeout management for all operations

### Virtual Media Information

The enhanced client provides detailed virtual media information:

```go
type VirtualMediaInfo struct {
    ConnectedVia string   `json:"ConnectedVia"`
    Image        string   `json:"Image"`
    ImageName    string   `json:"ImageName"`
    Inserted     bool     `json:"Inserted"`
    MediaTypes   []string `json:"MediaTypes"`
}
```

## Usage Examples

### Basic Virtual Media Management

```bash
# Check virtual media status
./openshift-sno-hub-installer virtual-media-info

# Eject current virtual media
./openshift-sno-hub-installer eject-media

# Insert new ISO
./openshift-sno-hub-installer insert-media http://example.com/install.iso

# Set boot to virtual CD (enhanced)
./openshift-sno-hub-installer set-boot-cd-enhanced

# Restart system
./openshift-sno-hub-installer restart
```

### Complete Virtual Media Boot Process

```bash
# Manage complete virtual media boot process
./openshift-sno-hub-installer manage-virtual-boot http://example.com/install.iso
```

### OpenShift Installation with Enhanced Virtual Media

```bash
# Full installation with enhanced virtual media support
./openshift-sno-hub-installer install
```

## Testing and Validation

### Test Script

A comprehensive test script is provided to validate the enhanced virtual media functionality:

```bash
./scripts/test_virtual_media.sh
```

**Test Coverage**:
- Virtual media information retrieval
- Enhanced virtual CD boot configuration
- Virtual media eject/insert operations
- Complete virtual media boot process

### Manual Testing

You can test the enhanced functionality manually:

```bash
# Test enhanced boot configuration
./openshift-sno-hub-installer set-boot-cd-enhanced

# Check virtual media status
./openshift-sno-hub-installer virtual-media-info

# Test complete process
./openshift-sno-hub-installer manage-virtual-boot http://example.com/test.iso
```

## Compatibility

### iDRAC 8 Support

The enhanced implementation is specifically designed for iDRAC 8 systems and includes:

- **RemoteCd Support**: Primary virtual media boot option for iDRAC 8
- **VirtualCd Support**: Alternative virtual media boot option
- **Fallback Support**: Physical CD support for compatibility

### Backward Compatibility

The original `set-boot-cd` command is still available and now uses the enhanced implementation internally, ensuring backward compatibility while providing improved functionality.

## Configuration

### iDRAC Configuration

Ensure your iDRAC configuration includes the virtual media settings:

```yaml
idrac:
  ip: "192.168.1.228"
  username: "root"
  password: "your-password"
  verify_ssl: false
  timeout: 30
```

### Virtual Media Requirements

- iDRAC 8 firmware with virtual media support
- Network access to ISO files
- Proper iDRAC user permissions for virtual media operations

## Troubleshooting

### Common Issues

1. **Virtual Media Not Inserted**
   - Check virtual media status with `virtual-media-info`
   - Ensure ISO URL is accessible
   - Verify iDRAC virtual media permissions

2. **Boot Configuration Fails**
   - Check iDRAC firmware version
   - Verify virtual media is properly inserted
   - Check iDRAC user permissions

3. **Network Issues**
   - Verify ISO URL accessibility
   - Check network connectivity to iDRAC
   - Ensure proper firewall configuration

### Debug Information

Enable debug logging to get detailed information:

```bash
# Check virtual media status
./openshift-sno-hub-installer virtual-media-info

# Test enhanced boot configuration
./openshift-sno-hub-installer set-boot-cd-enhanced
```

## Benefits

### Improved Reliability

- **Multiple Boot Options**: Tries different virtual CD options automatically
- **Better Error Handling**: Comprehensive error handling and fallback
- **Status Verification**: Checks virtual media status before operations

### Enhanced User Experience

- **Detailed Logging**: Clear information about each operation
- **Progress Tracking**: Step-by-step process information
- **Error Recovery**: Automatic fallback to alternative options

### iDRAC 8 Optimization

- **Native Support**: Optimized for iDRAC 8 virtual media features
- **Proper Boot Configuration**: Uses correct virtual media boot options
- **Enhanced Compatibility**: Works with various iDRAC 8 firmware versions

## Future Enhancements

- **Persistent Boot Configuration**: Option to set persistent virtual CD boot
- **Boot Option Discovery**: Automatic discovery of available boot options
- **Virtual Media Validation**: Enhanced validation of virtual media operations
- **Multi-Media Support**: Support for multiple virtual media types

This enhanced virtual media support ensures that OpenShift SNO hub installations work correctly with iDRAC 8 virtual media, providing a robust and reliable solution for automated server management.
