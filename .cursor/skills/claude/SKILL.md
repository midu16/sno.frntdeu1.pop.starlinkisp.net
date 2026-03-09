---
name: sno-infrastructure
description: Generate OpenShift SNO configs, review infrastructure code, and run installations for the sno.frntdeu1.pop.starlinkisp.net environment. Use when working with install-config.yaml, agent-config.yaml, iDRAC management, Go installer code, deployment scripts, or OpenShift cluster operations.
---

# SNO Infrastructure Skill

## Project Overview

Air-gapped Single Node OpenShift (SNO) deployment on Dell hardware (iDRAC 8) at `sno.frntdeu1.pop.starlinkisp.net`. Two installation paths exist:

- **Bash installer**: `install_openshift_sno_hub.sh` — end-to-end script
- **Go installer**: `go-installer/` — structured Go application with CLI subcommands

## Key Paths

| Path | Purpose |
|------|---------|
| `abi-master-0/install-config.yaml` | OCP install config template (has `<pull_secret>` and `<ssh_key>` placeholders) |
| `abi-master-0/agent-config.yaml` | Agent-based installer config (network, disk hints, NTP) |
| `abi-master-0/openshift/` | Extra manifests (PAO, operators) applied at install time |
| `install_openshift_sno_hub.sh` | Bash installer (config templating, ISO build, iDRAC boot, wait-for-complete) |
| `go-installer/` | Go installer (`main.go`, `internal/`) with `idrac_config.yaml` |
| `go-webcache/` | Local HTTP file server for serving ISOs to the BMC |
| `workdir/` | Generated artifacts (ISO, kubeconfig, state) — gitignored |
| `idrac_pw.enc` | AES-256-CBC encrypted iDRAC password — gitignored |
| `test-apps/` | Sample workload manifests (LVM, bind mounts) |

## Generating Configs

### install-config.yaml

Template lives at `abi-master-0/install-config.yaml`. Key fields:

```yaml
baseDomain: frntdeu1.pop.starlinkisp.net
metadata:
  name: sno
controlPlane:
  replicas: 1
compute:
  - replicas: 0
networking:
  networkType: OVNKubernetes
  clusterNetwork:
    - cidr: 10.128.0.0/14
      hostPrefix: 23
  serviceNetwork:
    - 172.30.0.0/16
  machineNetwork:
    - cidr: 192.168.1.0/24
platform:
  none: {}
pullSecret: '<replaced at install time>'
sshKey: '<replaced at install time>'
```

When generating or modifying:
- `pullSecret` and `sshKey` are templated from `~/.docker/config.json` and `~/.ssh/id_ed25519.pub` by the installer
- Never commit real pull secrets or SSH keys
- `compute.replicas` must be `0` for SNO
- `controlPlane.replicas` must be `1` for SNO

### agent-config.yaml

Template at `abi-master-0/agent-config.yaml`. Key fields:

```yaml
rendezvousIP: 192.168.1.133
hosts:
  - hostname: master-0
    role: master
    rootDeviceHints:
      deviceName: "/dev/disk/by-path/pci-0000:02:00.0-scsi-0:0:0:0"
    interfaces:
      - name: eno1np0
        macAddress: "84:16:0c:2a:83:fe"
    networkConfig:
      interfaces:
        - name: eno1np0
          type: ethernet
          state: up
          ipv4:
            address:
              - ip: 192.168.1.133
                prefix-length: 24
            dhcp: false
      dns-resolver:
        config:
          server:
            - 192.168.1.1
      routes:
        config:
          - destination: 0.0.0.0/0
            next-hop-address: 192.168.1.1
```

When generating or modifying:
- `rendezvousIP` must match the host's static IP
- `rootDeviceHints.deviceName` must match the target disk by-path
- `macAddress` must match the physical NIC
- `networkConfig` uses nmstate format (requires `nmstatectl` on the build host)
- NTP source (`additionalNTPSources`) should point to a reachable time server

## Reviewing Infrastructure Code

### Bash Script Review Checklist

When reviewing `install_openshift_sno_hub.sh`:
- Uses `set -euo pipefail` — verify no unset variable references
- iDRAC password sourced from `IDRAC_PW` env or decrypted `idrac_pw.enc`
- All `curl` calls to iDRAC use `-sk` (insecure TLS for BMC)
- Redfish endpoints target iDRAC 8 paths (`/redfish/v1/...`)
- ISO URL must be HTTP (not HTTPS) — BMC fetches over plain HTTP
- `racadm` fallback logic handles system-installed vs local binary

### Go Installer Review Checklist

When reviewing `go-installer/`:
- Config loaded from `idrac_config.yaml` (gitignored — contains credentials)
- Entry point: `main.go` → `internal/app`, `internal/config`, `internal/logger`
- CLI subcommands: `install`, `config`, `power-on`, `power-off`, `restart`, `status`, `info`, `eject-media`, `insert-media`, `set-boot-cd`, `set-boot-hdd`, `cleanup`
- Build: `cd go-installer && go build -o openshift-sno-hub-installer main.go`
- Uses Redfish API — same iDRAC 8 endpoints as the bash script

### YAML / Manifest Review

- Validate YAML syntax before committing
- Ensure no secrets are hardcoded (pull secrets, passwords, SSH keys)
- Cross-check `machineNetwork.cidr`, `rendezvousIP`, and `networkConfig` IP consistency
- Verify `rootDeviceHints` matches actual hardware

## Running Installs

### Prerequisites

| Tool | Purpose |
|------|---------|
| `oc` | OpenShift CLI — extract installer, manage cluster |
| `nmstatectl` | Validate nmstate network config in agent-config |
| `sshpass` | Non-interactive SSH key copy |
| `jq` | Parse JSON from iDRAC API |
| `racadm` / `idracadm7` | Dell iDRAC CLI for boot config (optional fallback) |
| `openssl` | Decrypt `idrac_pw.enc` |

### Bash Installer Workflow

```bash
# Set iDRAC password (or let script prompt for encrypted file passphrase)
export IDRAC_PW='<password>'

# Run full install
./install_openshift_sno_hub.sh
```

Steps performed:
1. Ensure `nmstatectl` is installed
2. Generate/copy SSH key to remote webcache host
3. Extract `openshift-install` from OCP release
4. Template `install-config.yaml` with pull secret and SSH key
5. Copy configs to `workdir/`
6. Build agent ISO (`openshift-install agent create image`)
7. SCP ISO to webcache host
8. iDRAC: eject existing media, mount ISO via HTTP URL
9. Set boot to VirtualCD via racadm
10. Power cycle server
11. Wait for install-complete

### Go Installer Workflow

```bash
cd go-installer

# Generate default config
./openshift-sno-hub-installer config

# Edit idrac_config.yaml with correct values

# Run full install
./openshift-sno-hub-installer install
```

### Post-Install

```bash
export KUBECONFIG=$(pwd)/workdir/auth/kubeconfig

# Approve pending install plans for Day-2 operators
oc get installplan -A -o jsonpath='{range .items[?(@.spec.approved==false)]}{.metadata.namespace} {.metadata.name}{"\n"}{end}' \
  | xargs -n2 sh -c 'oc patch installplan $1 -n $0 --type merge -p "{\"spec\": {\"approved\": true}}"'
```

## Environment Details

| Parameter | Value |
|-----------|-------|
| Base domain | `frntdeu1.pop.starlinkisp.net` |
| Cluster name | `sno` |
| Node IP | `192.168.1.133` |
| iDRAC IP | `192.168.1.228` |
| Webcache host | `192.168.1.21` (user: `rock`) |
| ISO serve URL | `http://192.168.1.21:8080/OSs/agent.x86_64.iso` |
| Gateway / DNS | `192.168.1.1` |
| NTP source | `192.168.1.21` |
| NIC | `eno1np0` / `84:16:0c:2a:83:fe` |
| Disk | `/dev/disk/by-path/pci-0000:02:00.0-scsi-0:0:0:0` |

## Security Reminders

- Never commit `idrac_pw.enc`, `idrac_config.yaml`, pull secrets, SSH private keys, or `workdir/auth/`
- The `.gitignore` already covers these — verify before staging
- iDRAC credentials should be rotated after initial deployment
- Pull secrets expire — refresh from [console.redhat.com](https://console.redhat.com/openshift/install/pull-secret)
