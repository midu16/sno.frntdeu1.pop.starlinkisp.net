# OpenShift SNO Hub Installer - Go Implementation

## Project Overview

This project is a complete Go rewrite of the original bash script `install_openshift_sno_hub_enhanced.sh`. The Go implementation provides enhanced functionality, better error handling, structured logging, and comprehensive iDRAC 8 API integration.

## Project Structure

```
go-installer/
├── cmd/openshift-sno-hub-installer/main.go    # Main application entry point
├── internal/
│   ├── app/app.go                            # Main application logic and CLI interface
│   ├── config/config.go                      # Configuration management
│   ├── idrac/
│   │   ├── client.go                         # iDRAC 8 API client implementation
│   │   └── client_test.go                    # Unit tests for iDRAC client
│   ├── logger/logger.go                      # Structured logging implementation
│   ├── openshift/installer.go                # OpenShift installation operations
│   └── ssh/manager.go                        # SSH key and remote operations
├── scripts/
│   ├── test_go_application.sh                # Application testing script
│   └── validate_idrac_apis.sh                # iDRAC API validation script
├── go.mod                                    # Go module definition
├── go.sum                                    # Go module checksums
├── main.go                                   # Legacy main file (can be removed)
├── README.md                                 # Comprehensive documentation
├── Makefile                                  # Build and development commands
├── Dockerfile                                # Container build configuration
├── .gitignore                                # Git ignore rules
├── idrac_config.yaml                         # Example configuration file
└── openshift-sno-hub-installer               # Compiled binary
```

## Key Features

### iDRAC 8 API Integration
- **Complete Redfish API Support**: All major iDRAC 8 endpoints implemented
- **System Management**: Power control, boot configuration, health monitoring
- **Virtual Media Management**: ISO mounting, ejection, boot source override
- **Error Handling**: Comprehensive error handling with retry logic
- **SSL Support**: Configurable SSL certificate validation

### OpenShift Installation
- **Automated Workflow**: Complete OpenShift SNO hub installation process
- **Installer Extraction**: Automated OpenShift installer extraction from releases
- **ISO Generation**: Agent image creation and management
- **Remote Deployment**: Automated ISO copying to remote web servers

### SSH Management
- **Key Generation**: Automated SSH key generation and distribution
- **Remote Operations**: File copying and remote command execution
- **Security**: Secure key management and authentication

### Configuration Management
- **YAML Configuration**: Human-readable configuration files
- **Validation**: Comprehensive configuration validation
- **Defaults**: Sensible default values with customization options

### Logging and Monitoring
- **Structured Logging**: JSON-formatted logs with different levels
- **File and Console Output**: Dual output for debugging and monitoring
- **Progress Tracking**: Detailed progress information for long-running operations

## iDRAC 8 API Endpoints Implemented

### System Management
- `GET /redfish/v1/Systems/System.Embedded.1` - System information
- `POST /redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset` - Power control
- `PATCH /redfish/v1/Systems/System.Embedded.1` - Boot configuration

### Virtual Media Management
- `POST /redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.EjectMedia` - Eject media
- `POST /redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.InsertMedia` - Insert media

## CLI Commands

```bash
# Full installation (default)
./openshift-sno-hub-installer install

# Configuration management
./openshift-sno-hub-installer config

# Power management
./openshift-sno-hub-installer power-on
./openshift-sno-hub-installer power-off
./openshift-sno-hub-installer restart

# System information
./openshift-sno-hub-installer status
./openshift-sno-hub-installer info

# Virtual media management
./openshift-sno-hub-installer eject-media
./openshift-sno-hub-installer insert-media <ISO_URL>
./openshift-sno-hub-installer set-boot-cd
./openshift-sno-hub-installer set-boot-hdd

# Cleanup operations
./openshift-sno-hub-installer cleanup
./openshift-sno-hub-installer cleanup poweroff
```

## Testing and Validation

### Unit Tests
- Comprehensive unit tests for iDRAC client functionality
- Mock server implementation for testing
- Benchmark tests for performance validation

### Validation Scripts
- `scripts/validate_idrac_apis.sh`: Validates all iDRAC 8 API endpoints
- `scripts/test_go_application.sh`: Tests Go application functionality

### Build and Development
- `Makefile`: Comprehensive build and development commands
- `Dockerfile`: Container build configuration
- Go modules for dependency management

## Dependencies

### Core Dependencies
- `github.com/sirupsen/logrus`: Structured logging
- `gopkg.in/yaml.v3`: YAML configuration parsing

### System Requirements
- Go 1.21 or later
- OpenShift CLI (`oc`) installed and configured
- SSH client tools (`ssh`, `sshpass`, `scp`)
- Access to iDRAC 8 system
- Registry authentication file for OpenShift images

## Security Features

- **Password Management**: Secure password handling (encryption support planned)
- **SSH Key Management**: Automated SSH key generation and distribution
- **SSL Verification**: Configurable SSL certificate validation
- **Authentication**: Basic authentication for iDRAC API

## Error Handling

- **Graceful Shutdown**: Signal handling for clean termination
- **Context Cancellation**: Proper context propagation for timeouts
- **Retry Logic**: Automatic retries for transient failures
- **Validation**: Configuration and input validation
- **Recovery**: Automatic cleanup on failures

## Performance Optimizations

- **Concurrent Operations**: Parallel execution where possible
- **Connection Pooling**: Efficient HTTP client configuration
- **Timeout Management**: Configurable timeouts for all operations
- **Resource Management**: Proper cleanup and resource management

## Migration from Bash Script

The Go implementation provides significant improvements over the original bash script:

1. **Better Error Handling**: Comprehensive error handling with proper error propagation
2. **Structured Logging**: JSON-formatted logs with different levels
3. **Configuration Management**: YAML-based configuration with validation
4. **Type Safety**: Compile-time type checking and validation
5. **Performance**: Better performance and resource utilization
6. **Maintainability**: Modular design with clear separation of concerns
7. **Testing**: Comprehensive unit tests and validation scripts
8. **Documentation**: Extensive documentation and examples

## Future Enhancements

- **Password Encryption**: Support for encrypted password storage
- **Web Interface**: Optional web-based management interface
- **Metrics**: Prometheus metrics for monitoring
- **Multi-Node Support**: Support for multiple iDRAC systems
- **Configuration Templates**: Pre-built configuration templates for common scenarios

## Support and Maintenance

The Go implementation is designed for long-term maintenance and support:

- **Modular Architecture**: Easy to extend and modify
- **Comprehensive Testing**: High test coverage for reliability
- **Documentation**: Extensive documentation for developers and users
- **Error Reporting**: Detailed error messages and logging
- **Version Control**: Git-based version control with proper branching

This implementation provides a robust, maintainable, and feature-rich replacement for the original bash script while maintaining full compatibility with existing iDRAC 8 systems and OpenShift installation workflows.
