---
name: sno-infrastructure
description: Generate OpenShift SNO configs, review infrastructure code, and run installations for the sno.frntdeu1.pop.starlinkisp.net environment. Use when working with install-config.yaml, agent-config.yaml, iDRAC management via idrac_sushy.py, Makefile targets, or OpenShift cluster operations.
---

# SNO Infrastructure Skill

## Project Overview

Air-gapped Single Node OpenShift (SNO) deployment on Dell hardware (iDRAC) at `sno.frntdeu1.pop.starlinkisp.net`. The installer is a single Python CLI:

- **idrac_sushy.py** — end-to-end SNO installer and iDRAC management via Redfish (sushy + sushy-oem-idrac). Handles preflight, config templating, ISO build, SCP to webcache, virtual media, one-time boot, power, and `wait-for install-complete`.

## Key Paths

| Path | Purpose |
|------|---------|
| `abi-master-0/install-config.yaml` | OCP install config template (placeholders replaced at install time) |
| `abi-master-0/agent-config.yaml` | Agent-based installer config (network, disk hints, NTP) |
| `abi-master-0/openshift/` | Extra manifests (PAO, operators) applied at install time |
| `idrac_sushy.py` | Python CLI: full installer + iDRAC via sushy (Redfish) |
| `test_idrac_sushy.py` | Functional tests (pytest) |
| `Makefile` | Build/run targets for install steps, iDRAC ops, tests, lint |
| `requirements.txt` | Python deps (sushy, sushy-oem-idrac, pytest, flake8) |
| `.github/workflows/install.yml` | CI: checkout, make deps, make install (workflow_dispatch, OCP_VERSION input) |
| `workdir/` | Generated artifacts (ISO, kubeconfig, state) — gitignored |
| `idrac_pw.enc` | AES-256-CBC encrypted iDRAC password — gitignored |

## Generating Configs

### install-config.yaml

Template at `abi-master-0/install-config.yaml`. Key fields:

- `baseDomain`, `metadata.name`, `controlPlane.replicas: 1`, `compute[0].replicas: 0`
- `networking`: OVNKubernetes, clusterNetwork, serviceNetwork, machineNetwork
- `platform.none: {}`
- `pullSecret` and `sshKey` are templated at install time from `~/.docker/config.json` and `~/.ssh/id_ed25519.pub` by `idrac_sushy.py prepare-configs`

When modifying: never commit real pull secrets or SSH keys; keep compute 0 and controlPlane 1 for SNO.

### agent-config.yaml

Template at `abi-master-0/agent-config.yaml`. Key fields:

- `rendezvousIP` — must match the host’s static IP
- `hosts[0].rootDeviceHints.deviceName` — target disk by-path
- `hosts[0].interfaces` / `networkConfig` — nmstate format; requires `nmstatectl` on the build host
- `additionalNTPSources` — reachable NTP server(s)

Keep `machineNetwork`, `rendezvousIP`, and `networkConfig` IPs consistent.

## idrac_sushy.py — Implementation Summary

- **Lazy sushy import** — non-iDRAC subcommands run without sushy installed.
- **Libraries**: `sushy`, `sushy-oem-idrac` (Redfish + Dell OEM for VirtualCD boot).
- **Credentials**: `--ip`, `--user`, `--password` or env `IDRAC_IP`, `IDRAC_USER`, `IDRAC_PW`; password can also come from decrypting `idrac_pw.enc` (pbkdf2 then legacy OpenSSL).
- **Errors**: `InstallerError` for all failures; caught in `main()`.
- **External tools**: subprocess for `oc`, `openshift-install`, `scp`, `ssh-keygen`, `openssl`. No racadm, curl, or jq.
- **ISO URL**: must be HTTP (BMC fetches over HTTP). Default: `http://<remote_host>:8080/OSs/agent.x86_64.iso`.

### Subcommands

| Command | Description |
|--------|-------------|
| `preflight` | Check/install sushy, nmstatectl, oc, sshpass, openssl |
| `ensure-ssh-key` | Generate SSH key if missing; ssh-copy-id to webcache host |
| `extract-installer` | Extract `openshift-install` from OCP release image |
| `prepare-configs` | Clean workdir; copy and template install-config + agent-config |
| `build-iso` | `openshift-install agent create image` |
| `copy-iso` | SCP ISO to webcache host |
| `status` | System model, power state, virtual media info |
| `eject` | Eject virtual media from VirtualCD |
| `insert <iso_url>` | Insert ISO via HTTP URL into VirtualCD |
| `set-boot-cd` | One-time boot to VirtualCD (Dell OEM) |
| `set-boot-hdd` | One-time boot to HDD |
| `restart` | Force-restart server |
| `power-on` / `power-off` | Power control |
| `wait-power-on` | Poll until power state is On |
| `deploy <iso_url>` | eject → insert → set-boot-cd → restart → wait-power-on |
| `wait-install` | `openshift-install agent wait-for install-complete` |
| `install` | Full workflow: preflight → ensure-ssh-key → extract-installer → prepare-configs → build-iso → copy-iso → deploy (default ISO URL) → wait-install |

### Flags

- `--ip`, `--user`, `--password` — iDRAC
- `--ocp-version` — OCP version (default in script)
- `--iso-url` — for `install` (default: http://remote_host:8080/OSs/agent.x86_64.iso)
- `--workdir`, `--src-dir`, `--remote-host`, etc. — see `idrac_sushy.py --help`

## Makefile Targets

- **Setup**: `deps` (pip install sushy, pytest, flake8), `clean` (workdir, openshift-install, caches)
- **Full**: `install` — runs `idrac_sushy.py install`; use `OCP_VERSION=4.18.6` as needed
- **Steps**: `preflight`, `ssh-key`, `extract-installer`, `prepare-configs`, `build-iso`, `copy-iso`, `deploy` (requires `ISO_URL=`), `wait-install`
- **iDRAC**: `status`, `eject`, `set-boot-cd`, `set-boot-hdd`, `restart`, `power-on`, `power-off`, `wait-power-on`
- **Testing**: `test`, `test-verbose`, `test-coverage`, `lint`

Environment: `IDRAC_PW`, `IDRAC_IP`, `IDRAC_USER`; Make passes them to the script. Example: `make install IDRAC_PW='pass' OCP_VERSION=4.18.6`.

## Running Installs

### Prerequisites

| Tool | Purpose |
|------|---------|
| `oc` | Extract installer, cluster operations |
| `nmstatectl` | Validate agent-config networkConfig (installed by preflight on supported OS) |
| `sshpass` | Non-interactive SSH key copy to webcache host |
| `pytest` | `make test` |
| `sushy`, `sushy-oem-idrac` | Redfish iDRAC (pip / `make deps`) |
| `openssl` | Decrypt `idrac_pw.enc` when not using `IDRAC_PW` |

### Workflow

```bash
export IDRAC_PW='<password>'   # or use idrac_pw.enc passphrase when prompted

make install
# or with version:
make install OCP_VERSION=4.18.6
```

Individual steps:

```bash
make preflight
make prepare-configs
make build-iso
make copy-iso
make deploy ISO_URL=http://192.168.1.21:8080/OSs/agent.x86_64.iso
make wait-install
```

Direct Python:

```bash
python3 idrac_sushy.py status
python3 idrac_sushy.py insert http://192.168.1.21:8080/OSs/agent.x86_64.iso
python3 idrac_sushy.py set-boot-cd
python3 idrac_sushy.py deploy http://192.168.1.21:8080/OSs/agent.x86_64.iso
python3 idrac_sushy.py --ip 10.0.0.1 --ocp-version 4.17.0 install
```

### Testing

```bash
make test
make test-verbose
make test-coverage
make lint
```

### Post-Install

```bash
export KUBECONFIG=$(pwd)/workdir/auth/kubeconfig

# Approve pending install plans (Day-2 operators)
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
| ISO URL | `http://192.168.1.21:8080/OSs/agent.x86_64.iso` |
| Gateway / DNS | `192.168.1.1` |
| NTP | `192.168.1.21` |
| NIC | `eno1np0` / `84:16:0c:2a:83:fe` |
| Disk | `/dev/disk/by-path/pci-0000:02:00.0-scsi-0:0:0:0` |

## Security Reminders

- Do not commit `idrac_pw.enc`, pull secrets, SSH private keys, or `workdir/auth/`. `.gitignore` covers these.
- Rotate iDRAC credentials after deployment. Refresh pull secrets from [console.redhat.com](https://console.redhat.com/openshift/install/pull-secret).
