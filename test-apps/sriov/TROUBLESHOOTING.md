# SRIOV test – troubleshooting (cluster findings)

This document summarizes cluster checks for the SRIOV test deployment and why pods stay Pending on the current node.

## Quick checks

```bash
export KUBECONFIG=/path/to/auth/kubeconfig

# Pod and scheduling
oc get pods -n sriov-test-ns -o wide
oc get events -n sriov-test-ns --sort-by='.lastTimestamp'

# Node: SRIOV label and device plugin
oc get node master-0 -o jsonpath='{.metadata.labels}' | tr ',' '\n' | grep -E 'sriov|feature'

# Node: extended resources (must show openshift.io/sriov_*)
oc describe node master-0 | grep -A 20 "Capacity:\|Allocatable:"

# SRIOV operator
oc get sriovnetworknodepolicy -A
oc get sriovnetworknodestate -n openshift-sriov-network-operator master-0 -o yaml
oc get daemonset -n openshift-sriov-network-operator
oc logs -n openshift-sriov-network-operator daemonset/sriov-network-config-daemon --tail=50
```

## Findings on this cluster (master-0)

### 1. Pod status

- **sriov-test** deployment: `0/1` READY.
- **If the node does not have the SRIOV label:** Pod **Pending** with: `0/1 nodes are available: 1 node(s) didn't match Pod's node affinity/selector.` Add the label: `oc apply -f test-apps/sriov/node-label-master-0.yaml`, or use `oc apply -k test-apps/sriov/`.
- **After the label is present:** Pod may still be **Pending** with: `0/1 nodes are available: 1 Insufficient hugepages-1Gi, 1 Insufficient openshift.io/sriov_dpdk_eno2np1, 1 Insufficient openshift.io/sriov_netdevice_eno2np1.` That means the node matches the nodeSelector but has no allocatable SRIOV VFs (policies didn't find matching NICs) and no hugepages. On this cluster, master-0 has no eno2np1 and only unsupported Broadcom NICs, so the deployment will stay 0/1 until a node with supported SRIOV NICs and (if needed) hugepages is used.

### 2. Node labels and resources

- master-0 has: `sriovnetwork.openshift.io/device-plugin=Disabled` (device plugin not enabled).
- master-0 does **not** have: `feature.node.kubernetes.io/network-sriov.capable: "true"`.
- Node **Capacity/Allocatable**: no `openshift.io/sriov_netdevice_eno2np1` or `openshift.io/sriov_dpdk_eno2np1` (no SRIOV resources advertised).

### 3. SRIOV operator state

- **Policies** exist and target PF **eno2np1**:
  - `sriov-config-netdevice-eno2np1`: eno2np1#0-29, resource `sriov_netdevice_eno2np1`.
  - `sriov-config-dpdk-eno2np1`: eno2np1#30-49, resource `sriov_dpdk_eno2np1`.
- **SriovNetworkNodeState** for master-0: `syncStatus: Succeeded`, state **Idle** – no policy has been applied (no matching nodeSelector).
- **sriov-device-plugin** DaemonSet: **0** desired/current (nodeSelector requires `sriovnetwork.openshift.io/device-plugin=Enabled`; no node has it).
- **sriov-network-config-daemon**: Running on master-0; logs show **no interface named eno2np1** and all discovered NICs reported as **unsupported**.

### 4. Hardware (config daemon logs)

The config daemon discovers only **Broadcom** NICs:

| PCI address  | Driver | Product |
|-------------|--------|---------|
| 0000:01:00.0 / 01:00.1 | bnxt_en | BCM57416 NetXtreme-E 10G RDMA |
| 0000:06:00.0 / 06:00.1 | tg3      | NetXtreme BCM5720 Gigabit Ethernet |

- All are reported as **unsupported model** (vendor 14e4 = Broadcom). The OpenShift SRIOV operator supports **Intel** and **Mellanox** NICs, not Broadcom.
- There is **no interface eno2np1** on this node. The policies are written for a different NIC naming (e.g. Intel or Mellanox); that PF does not exist here.

## Root cause

1. **No SRIOV-capable label** – So no policy is applied, device plugin stays disabled, and no SRIOV resources are advertised.
2. **Wrong hardware for this policy** – Policies target **eno2np1**. On master-0, **eno2np1 exists** (it is 0000:01:00.1, Broadcom BCM57416), but: (a) the OpenShift SRIOV operator does **not** support Broadcom (Intel/Mellanox only); (b) none of the node’s NICs expose `sriov_totalvfs` in sysfs, so no VFs can be configured. See **NIC-DISCOVERY.md** for the full `oc debug node` discovery.

## What works on this cluster

- Use the **smoke** deployment (no SRIOV; runs on any node):
  ```bash
  oc apply -f test-apps/sriov/sriov-test-deployment-smoke.yaml
  oc get pods -n sriov-test-ns
  ```
- The main **sriov-test** deployment will stay 0/1 until you have a node that:
  - Has a **supported** SRIOV NIC (Intel or Mellanox) and
  - Exposes a PF that matches the policy (e.g. same name as eno2np1, or you change the policy to match that node’s PF name/vendor/device ID).

## If you get a node with supported SRIOV (e.g. Intel eno2np1)

1. Ensure the node gets the capability label (often via Node Feature Discovery or the operator once a matching policy exists).
2. Apply or adjust policies so `nicSelector` (e.g. `pfNames` or vendor/deviceID) matches that node’s PF.
3. After the operator applies the policy and enables the device plugin, the node will advertise `openshift.io/sriov_netdevice_eno2np1` and `openshift.io/sriov_dpdk_eno2np1`, and the sriov-test pod will be able to schedule.
