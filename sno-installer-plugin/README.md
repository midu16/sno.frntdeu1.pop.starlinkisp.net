# SNO Installer Claude Plugin

This plugin extends Claude Code with specialized capabilities for automating the installation and management of Single Node OpenShift (SNO) clusters using the Agent-Based Installer and iDRAC management.

## Features

### Commands

#### `/install-sno`
Performs a complete, end-to-end SNO installation. It automates:
- Preflight checks
- Extraction of the OpenShift installer
- Configuration templating
- Agent ISO creation
- Transferring the ISO to the webcache host
- iDRAC virtual media insertion and boot configuration
- Monitoring the installation progress

**Arguments:**
- `idrac_pw` (required): The password for the target iDRAC.
- `ocp_version` (optional): The OpenShift version to install (e.g., `4.18.6`).

### Skills

#### `sno-installer-skill`
A specialized subagent for granular SNO management. You can use this skill to perform individual steps of the installation process or to manage the cluster after deployment.

**Supported Actions:**
- `preflight`: Run prerequisite checks.
- `build-iso`: Regenerate the Agent ISO.
cal
- `deploy`: Perform the iDRAC deployment cycle (eject, insert, boot, restart).
- `status`: Check the current iDRAC system status and virtual media state.
- `eject`: Eject virtual media from the iDRAC.
- `power-on` / `power-off`: Manage the power state of the server.

## Installation

To use this plugin in Claude Code, you can add the plugin directory to your Claude Code configuration or install it from the marketplace (if available).

```bash
# Example of adding the plugin directory locally
claude plugin add ./sno-launcher-plugin
```

## Requirements

- **Claude Code**
- **Node.js** (>= 18.0.0)
- **Access to the SNO Installer Repository**: The plugin expects to be run within or have access to the directory containing the `Makefile` and `idrac_sushy.py` script.
- **Environment Variables**: For certain operations, you may need to set `IDRAC_IP`, `IDRAC_USER`, etc.

## License

This plugin is provided as-is.
