# OpenShift Single Node OpenShift (SNO) Configuration

This directory contains the configuration files for deploying an OpenShift Single Node OpenShift cluster named `sno` on the `frntdeu1.pop.starlinkisp.net` domain.

## Overview

This SNO configuration is designed for a single master node deployment with various operators and customizations pre-installed. The cluster is configured with:

- **Base Domain**: `frntdeu1.pop.starlinkisp.net`
- **Cluster Name**: `sno`
- **Node Hostname**: `master-0`
- **Network**: `192.168.1.0/24` with static IP `192.168.1.133`
- **Architecture**: AMD64

## Directory Structure

```
abi-master-0/
├── agent-config.yaml          # Assisted Installer agent configuration
├── install-config.yaml        # OpenShift installation configuration
├── extra-manifests/           # Additional operators and configurations
│   ├── operator-config/       # Operator configuration manifests
│   └── operator-install/      # Operator installation manifests
└── openshift/                 # OpenShift-specific configurations
```

## Configuration Files

### Core Configuration

- **`agent-config.yaml`**: Assisted Installer configuration defining the single master node with network settings, NTP sources, and hardware specifications
- **`install-config.yaml`**: OpenShift installation configuration with cluster networking, platform settings, and authentication

### Extra Manifests

#### Operator Install (`extra-manifests/operator-install/`)

The following operators are configured for installation:

1. **`99_01_argo.yaml`** - ArgoCD GitOps Operator
2. **`99_02_logging.yaml`** - OpenShift Logging (ELK Stack)
3. **`99_03_lvms.yaml`** - Logical Volume Manager Storage
4. **`99_04_ptp.yaml`** - Precision Time Protocol
5. **`99_05_sriov.yaml`** - Single Root I/O Virtualization
6. **`99_06_quay.yaml`** - Red Hat Quay Container Registry
7. **`99_07_minio.yaml`** - MinIO Object Storage

#### Operator Configuration (`extra-manifests/operator-config/`)

- **`lvms-lvm-cluster.yaml`** - LVM Storage cluster configuration
- **`monitoring-config-cm.yaml`** - Monitoring configuration
- **`odf-mcg.yaml`** - OpenShift Data Foundation Multi-Cloud Gateway
- **`sriov-config-netdevice-eno2np1.yaml`** - SR-IOV network device configuration

### OpenShift Configurations (`openshift/`)

Various OpenShift-specific configurations including:

- **Container Runtime**: CRI-O capabilities and configuration
- **Networking**: DHCP disable, cluster network checks
- **Performance**: Workload partitioning, accelerated container startup
- **System**: Chrony configuration, kdump, systemd pstore
- **Kernel Modules**: MPLS, SCTP module loading
- **Power Management**: Power save settings

## Network Configuration

- **Management Network**: `192.168.1.0/24`
- **Node IP**: `192.168.1.133`
- **Gateway**: `192.168.1.1`
- **DNS Server**: `192.168.1.1`
- **NTP Server**: `192.168.1.21`
- **Interface**: `eno1np0` (MAC: `84:16:0c:2a:83:fe`)

## Storage Configuration

- **Root Device**: `/dev/disk/by-path/pci-0000:02:00.0-scsi-0:0:0:0`
- **LVM Storage**: Configured for local storage management
- **MinIO**: 100Gi persistent volume for object storage

## Deployment

This configuration is designed to work with the Assisted Installer for OpenShift. The files should be used with the appropriate installation scripts in the parent directory.

## Security Notes

- The configuration includes pull secrets for accessing Red Hat registries
- SSH keys are configured for cluster access
- FIPS mode is disabled by default
- Various security-related configurations are applied through the OpenShift manifests

## Monitoring and Observability

- OpenShift Logging (ELK Stack) is configured
- Cluster monitoring is enabled
- MinIO provides object storage for logs and metrics

## GitOps Integration

ArgoCD is configured to enable GitOps workflows for cluster management and application deployment.

## Customizations

This SNO configuration includes several customizations:

- CPU partitioning mode set to `AllNodes`
- Hyperthreading enabled
- OVN Kubernetes networking
- Various performance optimizations
- Custom kernel modules and system configurations

## Usage

Refer to the main project README.md for deployment instructions and usage examples.
