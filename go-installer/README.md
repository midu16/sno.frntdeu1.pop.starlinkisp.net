# OpenShift SNO Hub Installer

A Go-based tool for installing OpenShift Single Node OpenShift (SNO) Hub with enhanced iDRAC 8 management capabilities. This tool replaces the original bash script with a more robust, maintainable, and feature-rich implementation.

## Features

### Enhanced Virtual Media Support
- **iDRAC 8 Virtual Media**: Proper virtual CD/DVD/ISO management
- **Multiple Boot Options**: Automatic fallback between RemoteCd, VirtualCd, and Cd
- **Virtual Media Information**: Detailed status and configuration information
- **Enhanced Error Handling**: Comprehensive error handling with graceful fallback
- **Complete Boot Process**: End-to-end virtual media boot management

- **iDRAC 8 API Integration**: Full Redfish API support for Dell iDRAC 8 management
- **OpenShift Installation**: Automated OpenShift SNO hub installation
- **SSH Management**: Automated SSH key generation and distribution
- **Virtual Media Management**: Automated ISO mounting and boot management
- **Configuration Management**: YAML-based configuration with validation
- **Structured Logging**: Comprehensive logging with different levels
- **CLI Interface**: Command-line interface with multiple operation modes

## iDRAC 8 API Endpoints

The tool implements all major iDRAC 8 Redfish API endpoints:

### System Management
- `GET /redfish/v1/Systems/System.Embedded.1` - Get system information
- `POST /redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset` - System power control
- `PATCH /redfish/v1/Systems/System.Embedded.1` - Boot configuration

### Virtual Media Management
- `POST /redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.EjectMedia` - Eject media
- `POST /redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.InsertMedia` - Insert media

## Installation

### Prerequisites

- Go 1.21 or later
- OpenShift CLI (`oc`) installed and configured
- SSH client tools (`ssh`, `sshpass`, `scp`)
- Access to iDRAC 8 system
- Registry authentication file for OpenShift images

### Build

```bash
git clone <repository-url>
cd openshift-sno-hub-installer
go mod tidy
go build -o openshift-sno-hub-installer cmd/openshift-sno-hub-installer/main.go
```

## Configuration

### Initial Setup

1. Create a configuration file:
```bash
./openshift-sno-hub-installer config
```

2. Edit the generated `idrac_config.yaml` file with your settings:

```yaml
idrac:
  ip: "192.168.1.228"
  username: "root"
  password: "your-password"
  verify_ssl: false
  timeout: 30

openshift:
  version: "4.16.45"
  cluster_name: "sno-hub"
  registry_auth_file: "./config.json"

remote:
  user: "rock"
  host: "192.168.1.21"
  path: "/apps/webcache/OSs/"
  iso_url: "http://192.168.1.21:8080/OSs/agent.x86_64.iso"

paths:
  workdir: "./workdir"
  source_dir: "./abi-master-0"
  ssh_key_path: "/home/user/.ssh/id_ed25519.pub"
  installer_path: "./openshift-install"
```

### Required Files

- `config.json` - OpenShift registry authentication file
- `abi-master-0/openshift/` - OpenShift configuration directory
- `abi-master-0/agent-config.yaml` - Agent configuration
- `abi-master-0/install-config.yaml` - Installation configuration

## Usage

### Commands

```bash
# Full installation (default)
./openshift-sno-hub-installer install

# Create configuration file
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

# Cleanup
./openshift-sno-hub-installer cleanup
./openshift-sno-hub-installer cleanup poweroff
```

### Full Installation Process

The installation process includes:

1. **Connectivity Check**: Verify iDRAC connectivity
2. **System Information**: Gather system details and health status
3. **SSH Setup**: Generate and distribute SSH keys
4. **Installer Extraction**: Extract OpenShift installer from release
5. **Work Directory Preparation**: Set up installation workspace
6. **Agent Image Creation**: Generate OpenShift agent ISO
7. **Remote Copy**: Copy ISO to remote web server
8. **Boot Management**: Configure iDRAC boot settings and restart
9. **Installation Monitoring**: Monitor installation progress
10. **Cleanup**: Clean up virtual media and reset boot settings

## iDRAC 8 API Validation

All iDRAC 8 API endpoints have been validated and tested:

### System Endpoints
- ✅ System information retrieval
- ✅ Power state management (On/Off/Restart)
- ✅ Boot configuration (CD/HDD)
- ✅ Health status monitoring

### Virtual Media Endpoints
- ✅ Media ejection
- ✅ Media insertion with ISO URLs
- ✅ Boot source override configuration

### Error Handling
- ✅ Connection timeout handling
- ✅ SSL certificate validation (configurable)
- ✅ HTTP status code validation
- ✅ JSON response parsing
- ✅ Retry logic for transient failures

## Logging

The tool provides comprehensive logging:

- **File Logging**: All logs written to `logs/openshift_sno_hub_install.log`
- **Console Output**: Real-time console output with colors
- **Log Levels**: INFO, WARN, ERROR, DEBUG, SUCCESS
- **Structured Format**: Timestamped logs with context

## Error Handling

- **Graceful Shutdown**: Signal handling for clean termination
- **Context Cancellation**: Proper context propagation for timeouts
- **Retry Logic**: Automatic retries for transient failures
- **Validation**: Configuration and input validation
- **Recovery**: Automatic cleanup on failures

## Security Features

- **Password Management**: Secure password handling (encryption support planned)
- **SSH Key Management**: Automated SSH key generation and distribution
- **SSL Verification**: Configurable SSL certificate validation
- **Authentication**: Basic authentication for iDRAC API

## Troubleshooting

### Common Issues

1. **iDRAC Connection Failed**
   - Verify IP address and credentials
   - Check network connectivity
   - Ensure iDRAC is enabled and accessible

2. **OpenShift Installer Not Found**
   - Verify `oc` CLI is installed and in PATH
   - Check registry authentication file
   - Ensure OpenShift version is available

3. **SSH Key Issues**
   - Check SSH key permissions
   - Verify remote host accessibility
   - Ensure `sshpass` is installed

4. **Virtual Media Issues**
   - Verify ISO URL is accessible
   - Check iDRAC virtual media settings
   - Ensure sufficient iDRAC storage

### Debug Mode

Enable debug logging by setting the log level in the configuration or using environment variables.

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Support

For issues and questions:
- Create an issue in the repository
- Check the troubleshooting section
- Review the logs for detailed error information