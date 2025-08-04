# ABI `master-0` deployment

This is the repo of `sno.frntdeu1.pop.starlinkisp.net` OpenShift4 cluster

## Table of Contents
- [ABI `master-0` deployment](#abi-master-0-deployment)
  - [Table of Contents](#table-of-contents)
  - [How to install](#how-to-install)
    - [Obtaining the `openshift-install` cli:](#obtaining-the-openshift-install-cli)
    - [Obtaining the `agent.x86_64.iso` :](#obtaining-the-agentx86_64iso-)
    - [Upload the `agent.x86_64.iso` to the AirGapped webcache:](#upload-the-agentx86_64iso-to-the-airgapped-webcache)
    - [Mount the image to the server BMC:](#mount-the-image-to-the-server-bmc)
    - [Monitoring the installation process:](#monitoring-the-installation-process)
  - [Patching all the installplans](#patching-all-the-installplans)
  - [Installing the Hub Specific Operators on the `isolated` cores](#installing-the-hub-specific-operators-on-the-isolated-cores)
- [README.md Checklist](#readmemd-checklist)
  - [1. Project Overview](#1-project-overview)
  - [2. Architecture \& Components](#2-architecture--components)
  - [3. Installation \& Setup](#3-installation--setup)
  - [4. Usage](#4-usage)
  - [5. Performance \& Scaling](#5-performance--scaling)
  - [6. Testing \& Validation](#6-testing--validation)
  - [7. Troubleshooting](#7-troubleshooting)
  - [8. Contribution Guidelines](#8-contribution-guidelines)
  - [9. Licensing \& References](#9-licensing--references)


## How to install

Ensure that the [`agent-config.yaml`](./abi-master-0/agent-config.yaml) and [`install-config.yaml`](./abi-master-0/install-config.yaml) are allign with the environment.


Note, ensure that [`install-config.yaml`](./abi-master-0/install-config.yaml) has the updated [`pull-secret`](https://console.redhat.com/openshift/install/pull-secret) and the `ssh-key`.


### Obtaining the `openshift-install` cli: 

```bash
oc adm release extract -a /home/midu/.docker/config.json --command=openshift-install quay.io/openshift-release-dev/ocp-release@sha256:6a653700eaae84e648f428c009de6aa6c9a3196600554947886083cf5280ed07
```

Note, replace the `quay.io/openshift-release-dev/ocp-release@sha256:6a653700eaae84e648f428c009de6aa6c9a31966005549` with the Openshift4 version desired, by obtaining the `sha256` from https://mirror.openshift.com/pub/openshift-v4/x86_64/clients/ocp/4.16.24/release.txt . In this tutorial situation, we are going to install the Openshift 4.16.24 version.

### Obtaining the `agent.x86_64.iso` :

```bash
./openshift-install agent create image --dir ./workdir/ --log-level debug
```

### Upload the `agent.x86_64.iso` to the AirGapped webcache:

```bash
scp -r $(pwd)/workdir/agent.x86_64.iso rock@192.168.1.21:/apps/webcache/OSs/
```
or use the [go-webcache](./go-webcache/README.md) from your workingstation and expose the `agent.x86_64.iso` file to the server BMC.

### Mount the image to the server BMC:

### Monitoring the installation process:

```bash
export KUBECONFIG=$(pwd)/workdir/auth/kubeconfig
./openshift-install agent wait-for install-complete --dir ./workdir/
```

Note, Once the installation its done, proceed by installing the day2-operators and configure it.

## Patching all the installplans

Once the SNO its available, the `day2-operators` are pending for Manual approval so the installation starts:

```bash
oc get installplan -A -o jsonpath='{range .items[?(@.spec.approved==false)]}{.metadata.namespace} {.metadata.name}{"\n"}{end}' \
| xargs -n2 sh -c 'oc patch installplan $1 -n $0 --type merge -p "{\"spec\": {\"approved\": true}}"' 
```

## Installing the Hub Specific Operators on the `isolated` cores

As configured on the [pao.yaml](./abi-master-0/openshift/pao.yaml) we are still having at least 44 cores available for the `workload`.


# README.md Checklist

Use this checklist when creating or updating a project README.

---

## 1. Project Overview
- [x] **Project Name**: Clearly stated at the top.
- [ ] **Description**: Concise explanation of what the project does.
- [ ] **Use Cases**: Why this project exists and problems it solves.
- [ ] **Status**: (e.g., Alpha, Beta, Production-ready).

---

## 2. Architecture & Components
- [ ] **High-Level Diagram**: Optional but recommended.
- [ ] **Key Components**: Briefly describe services, pods, or modules.
- [ ] **Dependencies**: List core dependencies (e.g., OpenShift version, kube-burner, Loki, etc.).
---

## 3. Installation & Setup
- [ ] **Prerequisites**:
  - [ ] Required tools (kubectl, oc, Helm, etc.)
  - [ ] Required cluster version / OS version
- [ ] **Installation Steps**: Step-by-step instructions.
- [ ] **Configuration**:
  - [ ] ConfigMaps and Secrets explained
  - [ ] Environment variables documented
  - [ ] Example configuration provided

---

## 4. Usage
- [ ] **Basic Commands**: Common CLI invocations or scripts.
- [ ] **Examples**: Realistic usage examples (YAML manifests, workload runs, etc.).
- [ ] **Logs & Monitoring**: How to check logs, metrics, or troubleshooting info.

---

## 5. Performance & Scaling
- [ ] **Supported Scale**: Max tested pods/namespaces/nodes.
- [ ] **Resource Requirements**: CPU/memory/storage/I/O requirements.
- [ ] **Tuning Tips**: Flags, sysctl params, or cluster settings.
- [ ] **Limitations**: Known bottlenecks or unsupported scenarios.

---

## 6. Testing & Validation
- [ ] **How to Run Tests**: Unit, integration, or performance tests.
- [ ] **Validation Checklist**: (e.g., pods running, metrics collected, logs available).
- [ ] **CI/CD Integration**: Links or instructions if automated.

---

## 7. Troubleshooting
- [ ] **Common Issues**: List error messages and solutions.
- [ ] **FAQ Section**: Short Q&A for known questions.
- [ ] **Debugging Commands**: Helpful `kubectl`, `oc`, or system commands.

---

## 8. Contribution Guidelines
- [ ] **How to Contribute**: PR process, coding standards, commit message format.
- [ ] **Issue Reporting**: Where and how to report bugs.
- [ ] **Code of Conduct**: (if applicable).

---

## 9. Licensing & References
- [ ] **License**: MIT, Apache 2.0, etc.
- [ ] **Acknowledgments**: Tools, libraries, or partners (e.g., Samsung collaboration).
- [ ] **References**: Links to docs, specs, or research.

---
