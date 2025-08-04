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

