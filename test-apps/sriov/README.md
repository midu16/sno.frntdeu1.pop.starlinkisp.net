# SRIOV test deployment (netdevice + DPDK)

Pods in this test use two SRIOV interfaces on PF `eno2np1`:

- **Netdevice** – kernel driver, normal network interface (e.g. for `ip`, TCP/UDP).
- **DPDK** – vfio-pci, for userspace DPDK apps (no kernel IP stack).

## Prerequisites

- SRIOV Network Operator installed.
- Existing netdevice policy: `sriov-config-netdevice-eno2np1` (VF range 0–29 on eno2np1), e.g. from `abi-master-0/extra-manifests/operator-config/sriov-config-netdevice-eno2np1.yaml`.
- At least one node with `feature.node.kubernetes.io/network-sriov.capable: "true"` and the SRIOV device plugin enabled (node label `sriovnetwork.openshift.io/device-plugin` should not be `Disabled`). If the node has no SRIOV-capable NIC or the operator has not configured it yet, pods will stay Pending.

## Apply order

### Option A: Kustomize (recommended)

Applies all manifests in the correct order (see `kustomization.yaml`):

```bash
oc apply -k test-apps/sriov/
oc adm policy add-scc-to-user privileged -z default -n sriov-test-ns
```

Order: namespace → node label (master-0) → DPDK policy → SriovNetworks → Deployment.

### Option B: Manual

From the repo root:

1. **Namespace:** `oc create -f test-apps/sriov/namespace.yaml`
2. **Node label** (so master-0 matches SRIOV nodeSelector): `oc apply -f test-apps/sriov/node-label-master-0.yaml`
3. **DPDK policy:** `oc create -f test-apps/sriov/sriov-dpdk-policy.yaml`
4. **SriovNetworks:** `oc create -f test-apps/sriov/sriov-networks.yaml`
5. **Privileged SCC:** `oc adm policy add-scc-to-user privileged -z default -n sriov-test-ns`
6. **Deployment:** `oc create -f test-apps/sriov/sriov-test-deployment.yaml`

## Manifests

| File | Purpose |
|------|--------|
| `kustomization.yaml` | Kustomize manifest list in apply order (namespace → node label → DPDK policy → SriovNetworks → deployment). |
| `namespace.yaml` | Namespace `sriov-test-ns` (privileged for SRIOV/DPDK). |
| `node-label-master-0.yaml` | Node manifest adding `feature.node.kubernetes.io/network-sriov.capable: "true"` to master-0 so it matches policy and pod nodeSelector. |
| `sriov-dpdk-policy.yaml` | SriovNetworkNodePolicy for DPDK (vfio-pci), resource `sriov_dpdk_eno2np1`, VF range 30–49. |
| `sriov-networks.yaml` | SriovNetwork for netdev (`sriov-netdevice-eno2np1`) and DPDK (`sriov-dpdk-eno2np1`) in `sriov-test-ns`. |
| `sriov-test-deployment.yaml` | Deployment with pods that request one netdev and one DPDK VF, plus hugepages. |
| `sriov-test-deployment-netdev-only.yaml` | Netdevice-only: one VF (kernel netdev), no DPDK, no hugepages. Use to validate VF attachment when the full deployment cannot schedule. |
| `sriov-test-deployment-smoke.yaml` | Smoke test: runs on any node without SRIOV (no nodeSelector/resources). Use to verify namespace and SCC when no SRIOV-capable node exists. |
| `validate-vf-attachment.sh` | Script: applies kustomize, grants SCC, waits for pod, then exec's `ip link`/`ip addr` to verify VF attachment. Optional arg `netdev-only` uses the netdevice-only deployment. |

## Validate VF attachment

**One-shot apply + verify (recommended):**

```bash
# From repo root, with KUBECONFIG set
./test-apps/sriov/validate-vf-attachment.sh
```

This applies `oc apply -k test-apps/sriov/`, grants the privileged SCC, waits for the deployment pod to be Running, then runs `ip -br link` and `ip -br addr` inside the pod to show the attached VF interface(s).

If the full deployment stays **Pending** (e.g. node has no 1Gi hugepages), use the netdevice-only deployment:

```bash
./test-apps/sriov/validate-vf-attachment.sh netdev-only
```

That uses `sriov-test-deployment-netdev-only.yaml` (one netdev VF, no DPDK, no hugepages) so the pod can schedule and you can confirm VF attachment.

**Manual verification:**

- List pods and check they have SRIOV resources and are Running:

  ```bash
  oc get pods -n sriov-test-ns -o wide
  oc describe pod -n sriov-test-ns -l app=sriov-test
  ```

- In a pod, check kernel netdevice (netdev) and that a VF interface exists (e.g. `net1`):

  ```bash
  oc exec -n sriov-test-ns deploy/sriov-test -- ip -br link show
  oc exec -n sriov-test-ns deploy/sriov-test -- ip -br addr show
  ```

- DPDK VF is exposed as a VFIO device (e.g. under `/dev/vfio/`) for use by DPDK apps in the container.

## Troubleshooting (0 pods / Pending)

If the deployment shows `0/1` and `oc get pods -n sriov-test-ns` shows no or Pending pods:

1. **Check ReplicaSet and events:**
   ```bash
   oc get replicaset -n sriov-test-ns
   oc get pods -n sriov-test-ns
   oc get events -n sriov-test-ns --sort-by='.lastTimestamp'
   oc describe deployment -n sriov-test-ns
   ```

2. **Confirm the node has SRIOV labels and resources:**
   ```bash
   oc get nodes -l feature.node.kubernetes.io/network-sriov.capable=true
   oc describe node <node-name>   # look at Capacity/Allocatable for openshift.io/sriov_netdevice_eno2np1 and openshift.io/sriov_dpdk_eno2np1
   ```
   If the node is missing the label, the SRIOV operator may not have detected the NIC yet, or Node Feature Discovery may not be running. If allocatable is 0 for either resource, the DPDK or netdevice policy may not be applied on that node yet (wait for the operator to reconcile and optionally reboot the node if required).

3. **Pod forbidden by SCC (privileged / capabilities):** Grant the default service account access to the privileged SCC:
   ```bash
   oc adm policy add-scc-to-user privileged -z default -n sriov-test-ns
   ```
   Then delete the ReplicaSet so the deployment creates a new pod: `oc delete replicaset -n sriov-test-ns --all`.

4. **Replicas** are set to 1 for single-node; increase in the deployment YAML if you have multiple SRIOV-capable nodes.

5. **No SRIOV-capable node (pod stays Pending):** Use the smoke deployment to confirm the rest works:
   ```bash
   oc apply -f test-apps/sriov/sriov-test-deployment-smoke.yaml
   oc get pods -n sriov-test-ns
   ```
   The main `sriov-test` deployment will stay 0/1 until at least one node has `feature.node.kubernetes.io/network-sriov.capable: "true"` and advertises the SRIOV extended resources.

6. **Hardware / policy mismatch:** The policies target PF **eno2np1**. If the node has no such interface, or only Broadcom NICs (unsupported by the SRIOV operator), pods will never get SRIOV. See **TROUBLESHOOTING.md** and **NIC-DISCOVERY.md** (from `oc debug node` discovery) for supported NICs (Intel, Mellanox).
