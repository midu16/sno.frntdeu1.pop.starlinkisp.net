# SRIOV test – troubleshooting (cluster findings)

## Quick checks

```bash
export KUBECONFIG=/path/to/kubeconfig

oc get pods -n sriov-test-ns -o wide
oc get events -n sriov-test-ns --sort-by='.lastTimestamp'
oc get node master-0 -o jsonpath='{.metadata.labels}' | tr ',' '\n' | grep -E 'sriov|feature'
oc describe node master-0 | grep -A 20 "Capacity:\|Allocatable:"
oc get sriovnetworknodepolicy -A
oc get sriovnetworknodestate -n openshift-sriov-network-operator master-0 -o yaml
oc logs -n openshift-sriov-network-operator daemonset/sriov-network-config-daemon --tail=50
```

## Current hardware findings (master-0)

| Item | Status |
|------|--------|
| PF `eno2np1` (BCM57416, `14e4:16d8`) | Present; listed in `supported-nic-ids` |
| Node label `feature.node.kubernetes.io/network-sriov.capable` | Applied via operator-config |
| Policy `sriov-config-netdevice-eno2np1` | Targets `kubernetes.io/hostname=master-0` |
| sysfs `sriov_totalvfs` | **Missing** → firmware reports TotalVfs=0 |
| Daemon error | `NumVfs (50) is larger than TotalVfs (0)` |

## Root cause for missing VFs

The Broadcom BCM57416 is recognized by the operator (via `supported-nic-ids`), but the NIC does **not** expose SR-IOV in sysfs. Typical causes:

1. **SR-IOV disabled in system BIOS / iDRAC** (most common on Dell) — enable global SR-IOV / NIC virtualization and reboot.
2. NIC firmware that does not advertise VFs until link/BIOS settings are correct.

Software manifests alone cannot create VFs when `sriov_totalvfs` is absent.

## Verify after enabling BIOS SR-IOV

```bash
# On the node (via config-daemon or oc debug):
cat /sys/bus/pci/devices/0000:01:00.1/sriov_totalvfs   # must be > 0

oc logs -n openshift-sriov-network-operator daemonset/sriov-network-config-daemon --tail=30
oc get sriovnetworknodestate master-0 -n openshift-sriov-network-operator -o yaml
oc describe node master-0 | grep openshift.io/sriov
```

## Smoke test without VFs

```bash
oc apply -f test-apps/sriov/sriov-test-deployment-smoke.yaml
```
