# SNO deployment for sno.frntdeu1.pop.starlinkisp.net

This repository automates the deployment of an **air-gapped Single Node OpenShift (SNO) cluster** on a **Dell PowerEdge R630** server at `sno.frntdeu1.pop.starlinkisp.net`. It uses the OpenShift agent-based installer, with iDRAC (Dell BMC) for virtual media and boot control.

[![SNO OpenShift Install](https://github.com/midu16/sno.frntdeu1.pop.starlinkisp.net/actions/workflows/install.yml/badge.svg)](https://github.com/midu16/sno.frntdeu1.pop.starlinkisp.net/actions/workflows/install.yml)

## Table of Contents

- [Overview](#overview)
- [Repository structure](#repository-structure)
- [Prerequisites](#prerequisites)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Installation workflow](#installation-workflow)
- [Post-install](#post-install)
- [Testing](#testing)
- [CI/CD](#cicd)
- [Use cases](#use-cases)

## Overview

- **Target hardware**: Dell PowerEdge R630 (iDRAC for out-of-band management).
- **Deployment**: Air-gapped SNO via the agent-based installer; ISO is built locally, served over HTTP to the BMC, and the node boots from virtual CD.
- **Automation**: A single Python CLI (`idrac_sushy.py`) drives the full flow: preflight, config templating, ISO build, SCP to webcache host, iDRAC virtual media and boot, and `wait-for install-complete`. iDRAC is accessed via Redfish using the [sushy](https://github.com/openstack/sushy) and [sushy-oem-idrac](https://github.com/openstack/sushy-oem-idrac) libraries.

## Repository structure

| Path | Purpose |
|------|---------|
| `idrac_sushy.py` | Main CLI: full install workflow and iDRAC operations (Redfish/sushy). |
| `test_idrac_sushy.py` | Pytest-based tests for the installer. |
| `Makefile` | Targets for `deps`, `install`, per-step commands, iDRAC ops, and tests. |
| `scripts/collect_abi_install_diagnostics.sh` | Bundles installer + `oc` diagnostics for CI (used on workflow failure). |
| `requirements.txt` | Python dependencies (sushy, pytest, etc.). |
| `abi-master-0/install-config.yaml` | OpenShift install config template (pull secret and SSH key are templated at run time). |
| `abi-master-0/agent-config.yaml` | Agent installer config (network, disk hints, NTP). |
| `abi-master-0/openshift/` | Extra manifests (e.g. PAO) applied at install time. |
| `abi-master-0/extra-manifests/` | Day-2 operator install and config manifests. |
| `.github/workflows/install.yml` | Optional CI: `workflow_dispatch` to run a full SNO install. |
| `workdir/` | Generated artifacts (ISO, kubeconfig, auth) — gitignored. |
| `.venv/` | Python virtual environment created by `make deps` — gitignored. |

## Prerequisites

- **oc** (OpenShift CLI) in PATH.
- **nmstate** (for `nmstatectl`) to validate `agent-config.yaml` network config — installed by preflight on supported OS, or via `pip install nmstate` when the system package is not available (e.g. Debian/Ubuntu without the package in repos).
- **sshpass** — for non-interactive SSH key copy to the webcache host; installed by preflight when possible.
- **openssl** — for optional decryption of `idrac_pw.enc`.
- **iDRAC credentials** — set `IDRAC_PW` (and optionally `IDRAC_IP`, `IDRAC_USER`) or use an encrypted password file.

Python dependencies (sushy, sushy-oem-idrac, pytest, flake8) are installed by `make deps`, which creates a `.venv` and installs into it to avoid system Python conflicts (e.g. externally-managed-environment on Debian/Ubuntu).

## Quick start

```bash
# Create venv and install Python deps
make deps

# Set iDRAC password (or use idrac_pw.enc and passphrase when prompted)
export IDRAC_PW='your-idrac-password'

# Run full SNO install (default OCP version is defined in idrac_sushy.py)
make install

# Or specify an OpenShift version
make install OCP_VERSION=4.19.23
```

The install workflow: preflight → ensure SSH key → extract `openshift-install` → prepare configs → build agent ISO → copy ISO to webcache host → iDRAC deploy (eject, insert, set boot to VirtualCD, restart, wait for power on) → wait for install-complete (with **API readiness gates** between retries so SNO MachineConfig reboots that produce `no route to host` / `connection refused` do not immediately burn another ~40m wait-for window — env `API_READY_WAIT_SEC` / `API_READY_SETTLE_SEC`; and optional **remediation**: if those waits exhaust but `workdir/auth/kubeconfig` exists, more `wait-for install-complete` rounds — env `REMEDIATION_INSTALL_WAIT_ATTEMPTS`, default `0` offline; the GitHub workflow defaults it to `3`).

## Configuration

- **Install config**: Edit `abi-master-0/install-config.yaml` for base domain, cluster name, and networking. `pullSecret` and `sshKey` are filled at run time from `~/.docker/config.json` and `~/.ssh/id_ed25519.pub`.
- **Agent config**: Edit `abi-master-0/agent-config.yaml` for rendezvous IP, hostname, root device hints, NIC/MAC, and nmstate `networkConfig`. Ensure NTP and network settings match the PowerEdge R630 environment (e.g. `192.168.1.133`, `eno1np0`).
- **iDRAC**: Default IP `192.168.1.228`; override with `IDRAC_IP`, `IDRAC_USER`, and `IDRAC_PW` (env or `--ip` / `--user` / `--password` in the script).

Default environment values (node IP, webcache host, ISO URL, NIC, disk) are defined in `idrac_sushy.py` and can be overridden via CLI or env where supported.

## Installation workflow

| Step | Make target | Description |
|------|-------------|-------------|
| 1 | `make preflight` | Check/install sushy, nmstatectl, sshpass, oc, openssl. |
| 2 | `make ssh-key` | Generate SSH key if missing; copy to webcache host. |
| 3 | `make extract-installer` | Extract `openshift-install` from OCP release. |
| 4 | `make prepare-configs` | Prepare `workdir` with templated configs. |
| 5 | `make build-iso` | Build agent ISO. |
| 6 | `make copy-iso` | SCP ISO to webcache host. |
| 7 | `make deploy ISO_URL=...` | iDRAC: eject → insert → set-boot-cd → restart → wait-power-on. |
| 8 | `make wait-install` | Run `openshift-install agent wait-for install-complete`. |

Individual iDRAC operations: `make status`, `make eject`, `make set-boot-cd`, `make set-boot-hdd`, `make restart`, `make power-on`, `make power-off`, `make wait-power-on`.

## Post-install

```bash
export KUBECONFIG=$(pwd)/workdir/auth/kubeconfig

# Install Day-2 operators (idempotent)
oc apply -f ./abi-master-0/extra-manifests/operator-install/

# Approve InstallPlans, wait for LVMS/SR-IOV, apply operator-config manifests
./.venv/bin/python3 scripts/apply_operator_config.py

# Optional: supplementary node_exporter metrics (see docs/node-exporter-zoneinfo*.md)
oc apply -k ./abi-master-0/extra-manifests/node-exporter-zoneinfo/
```

Hub-specific operators and isolated cores are configured via manifests under `abi-master-0/openshift/` (e.g. [pao.yaml](./abi-master-0/openshift/pao.yaml)) and `abi-master-0/extra-manifests/`.

## Testing

```bash
make test          # Run pytest
make test-verbose  # Pytest with stdout visible
make test-coverage # Pytest with coverage report
make lint          # flake8 on idrac_sushy.py and test_idrac_sushy.py
```

## CI/CD

The workflow [.github/workflows/install.yml](./.github/workflows/install.yml) provides a manual trigger (`workflow_dispatch`) to run a full SNO install on a self-hosted runner. It uses secrets for `IDRAC_PW` and workflow inputs for the OpenShift version, primary `INSTALL_WAIT_ATTEMPTS`, post-failure **`REMEDIATION_INSTALL_WAIT_ATTEMPTS`**, and **ISO/iDRAC pacing** (defaults: **120s** post-`scp`, **120s** after Virtual CD insert, **30s** after boot-order before `ForceRestart`, **45s** after restart; optional **HTTP range probe** of the ISO URL). On install failure it uploads **`abi-install-diagnostics`**. The job checks out the repo, runs `make deps`, then `make install` with `PATH` including `.venv/bin`. After the cluster is up it applies `operator-install`, validates cluster operators, runs `scripts/apply_operator_config.py`, and applies `node-exporter-zoneinfo`.

## Use cases

This project is used to:

- **Deploy and re-deploy SNO on a Dell PowerEdge R630** in an air-gapped environment at `sno.frntdeu1.pop.starlinkisp.net`, using iDRAC for virtual media and one-time boot.
- **Standardize configs** — keep `install-config.yaml`, `agent-config.yaml`, and extra manifests aligned with the environment.
- **Automate Day-2 operators** — apply operator manifests and approve install plans after the cluster is up.
- **Reduce manual steps** — single entry point (`make install` or `idrac_sushy.py install`) for the full flow from preflight to install-complete.
