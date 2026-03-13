# Exporting node_exporter zoneinfo, interrupts, and softirqs to cluster Prometheus (KCS)

## Issue

I need to collect **zoneinfo**, **interrupts**, and **softirqs** metrics from nodes in my OpenShift cluster and have them available in **cluster Prometheus** (Observe → Metrics). The cluster-monitoring-config ConfigMap does not support enabling these collectors for my OpenShift version, so the default node_exporter does not expose them.

## Environment

- **Product:** Red Hat OpenShift Container Platform 4.x (tested on 4.19)
- **Component:** Cluster Monitoring (Prometheus, node_exporter)
- **Relevant resources:** `abi-master-0/extra-manifests/node-exporter-zoneinfo/` (DaemonSet, Service, ServiceMonitor in `openshift-monitoring`)
- **Prerequisites:** Cluster monitoring stack installed; `kubeconfig` or `KUBECONFIG` set for cluster access

Example:

```bash
export KUBECONFIG=/path/to/kubeconfig
```

## Cause

The Cluster Monitoring Operator (CMO) validates `cluster-monitoring-config` against a fixed schema. Only collectors defined in that schema (for example `netdev`, `netclass`, `systemd`) can be enabled via the ConfigMap. Collectors such as **zoneinfo**, **interrupts**, and **softirqs** are not in the schema on many OCP versions, so they cannot be enabled through the CMO. A supplementary node_exporter that enables only these collectors and is scraped by cluster Prometheus is a supported workaround.

## Resolution

Follow these steps to deploy the supplementary node_exporter and confirm metrics are exported to cluster Prometheus.

### Step 1: Set cluster context

Ensure you can access the cluster:

```bash
export KUBECONFIG=/home/midu/Downloads/auth/kubeconfig
oc whoami
oc get nodes
```

**Expected output (example):**

```
system:admin
NAME       STATUS   ROLES           AGE   VERSION
master-0   Ready    master,worker   20h   v1.32.10
```

### Step 2: Deploy node-exporter-zoneinfo

From the repository root, apply the kustomization that deploys the DaemonSet, Service, and ServiceMonitor into `openshift-monitoring`:

```bash
oc apply -k abi-master-0/extra-manifests/node-exporter-zoneinfo/
```

**Expected output:**

```
daemonset.apps/node-exporter-zoneinfo created
service/node-exporter-zoneinfo created
servicemonitor.monitoring.coreos.com/node-exporter-zoneinfo created
```

(If resources already exist, you will see `configured` instead of `created`.)

### Step 3: (Optional) Force rollout if pods do not start immediately

The kustomization includes **clusterrolebinding-privileged-scc.yaml**, which grants the privileged SCC to the `default` service account in `openshift-monitoring` (equivalent to `oc adm policy add-scc-to-user privileged -z default -n openshift-monitoring`). The DaemonSet uses `serviceAccountName: default`, so no separate SCC command is required.

If the DaemonSet reports 0 pods (e.g. `DESIRED 1`, `CURRENT 0`) after the first apply, the controller may not have retried yet. Force a rollout by updating the DaemonSet pod template annotation and re-applying:

1. In **daemonset.yaml**, under `spec.template.metadata.annotations`, change the value of `node-exporter-zoneinfo/force-rollout` from `"1"` to `"2"` (or any other value).
2. Re-apply the kustomization:

   ```bash
   oc apply -k abi-master-0/extra-manifests/node-exporter-zoneinfo/
   ```

Check pod status:

```bash
oc get pods -n openshift-monitoring -l app.kubernetes.io/name=node-exporter-zoneinfo
oc get daemonset -n openshift-monitoring node-exporter-zoneinfo
```

### Step 4: Verify DaemonSet and pods

Confirm the DaemonSet is scheduled and the pod is running:

```bash
oc get daemonset -n openshift-monitoring node-exporter-zoneinfo
oc get pods -n openshift-monitoring -l app.kubernetes.io/name=node-exporter-zoneinfo
```

**Expected output (example):**

```
NAME                     DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR   AGE
node-exporter-zoneinfo   1         1         1       1            1           <none>          2m
```

```
NAME                           READY   STATUS    RESTARTS   AGE
node-exporter-zoneinfo-xxxxx   1/1     Running   0          2m
```

### Step 5: Validate metrics on the exporter (port 9101)

Port-forward to the DaemonSet pod and check that the zoneinfo, interrupts, and softirqs collectors expose metrics:

```bash
oc port-forward -n openshift-monitoring daemonset/node-exporter-zoneinfo 9101:9101
```

In another terminal:

```bash
curl -s http://localhost:9101/metrics | grep -E '^node_zoneinfo_|^node_intr_|^node_softirqs_' | head -20
```

**Expected output (example):** Lines for zoneinfo, interrupts, and softirqs (labels may vary; the `job="node-exporter-zoneinfo"` label is added by Prometheus when scraping):

```
node_zoneinfo_managed_pages{instance="localhost:9101",zone="DMA"} 0
node_zoneinfo_managed_pages{instance="localhost:9101",zone="DMA32"} 12345
...
node_intr_total{instance="localhost:9101",irq="NMI",cpu="0"} 123
...
node_softirqs_total{instance="localhost:9101",type="HI",cpu="0"} 0
...
```

Stop the port-forward with `Ctrl+C` in the first terminal.

### Step 6: Validate ServiceMonitor and Prometheus targets

Confirm the ServiceMonitor exists and that cluster Prometheus has a scrape target for the job:

```bash
oc get servicemonitor -n openshift-monitoring node-exporter-zoneinfo -o yaml
```

**Expected (relevant snippet):** `selector` matching the Service labels, `endpoints.port: metrics`, `jobLabel: app.kubernetes.io/name`.

Check that the Prometheus instance has discovered the target (target list or config is cluster-dependent; the following is a typical check):

```bash
oc get service -n openshift-monitoring node-exporter-zoneinfo
oc get endpoints -n openshift-monitoring node-exporter-zoneinfo
```

**Expected output (example):**

```
NAME                     TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)    AGE
node-exporter-zoneinfo   ClusterIP   172.30.xxx.xx   <none>        9101/TCP   5m
```

```
NAME                     ENDPOINTS          AGE
node-exporter-zoneinfo   10.128.x.x:9101    5m
```

### Step 7: Validate metrics in Prometheus (Observe → Metrics)

In the OpenShift web console:

1. Go to **Observe → Metrics** (cluster metrics).
2. Run a PromQL query that uses the supplementary exporter’s job label:

   ```promql
   node_intr_total{job="node-exporter-zoneinfo"}
   ```

   Or:

   ```promql
   node_zoneinfo_managed_pages{job="node-exporter-zoneinfo"}
   ```

   Or:

   ```promql
   node_softirqs_total{job="node-exporter-zoneinfo"}
   ```

3. Confirm that time series are returned and that the `job` label is `node-exporter-zoneinfo`.

**Expected:** Query returns series; graph or table shows data. No “No datapoints” for the selected time range once scraping has started.

### Step 8: Optional – rate queries for interrupts and softirqs

To validate that interrupts and softirqs are being scraped and are useful for graphing:

```promql
sum(rate(node_intr_total{job="node-exporter-zoneinfo"}[5m])) by (instance)
```

```promql
sum(rate(node_softirqs_total{job="node-exporter-zoneinfo"}[5m])) by (instance, type)
```

**Expected:** Non-empty result set (and, over time, non-zero rates if the node has activity).

## Verification summary

| Check | Command / action | Success criteria |
|-------|-------------------|------------------|
| DaemonSet | `oc get daemonset -n openshift-monitoring node-exporter-zoneinfo` | DESIRED=CURRENT=READY=AVAILABLE=1 |
| Pod | `oc get pods -n openshift-monitoring -l app.kubernetes.io/name=node-exporter-zoneinfo` | 1/1 Running |
| Metrics on port 9101 | `curl -s http://localhost:9101/metrics` (after port-forward) | Lines with `node_zoneinfo_*`, `node_intr_*`, `node_softirqs_*` |
| ServiceMonitor | `oc get servicemonitor -n openshift-monitoring node-exporter-zoneinfo` | Resource exists |
| Prometheus | Observe → Metrics, query `node_intr_total{job="node-exporter-zoneinfo"}` | Series returned |

## Additional information

- **Directory layout:** The `abi-master-0/extra-manifests/node-exporter-zoneinfo/` directory contains:
  - `clusterrolebinding-privileged-scc.yaml` – ClusterRoleBinding that grants the privileged SCC to the `default` service account in `openshift-monitoring` (so the DaemonSet can use hostPID, hostPath, runAsUser=0).
  - `daemonset.yaml` – DaemonSet (image `quay.io/prometheus/node-exporter:v1.9.1`, collectors zoneinfo, interrupts, softirqs, port 9101, `openshift-monitoring`, `serviceAccountName: default`; pod template annotation `node-exporter-zoneinfo/force-rollout` can be bumped to force a rollout).
  - `service.yaml` – Service for the DaemonSet pods (port 9101).
  - `servicemonitor.yaml` – ServiceMonitor so cluster Prometheus scrapes the Service (job label `node-exporter-zoneinfo`).
  - `kustomization.yaml` – Kustomize resource list for the four manifests.
  - `README.md` – Short apply and verify instructions.

- **Collectors enabled:** Only `--collector.zoneinfo`, `--collector.interrupts`, and `--collector.softirqs` (with `--collector.disable-defaults`). The main node_exporter (port 9100) is unchanged and continues to be managed by the CMO.

- **High cardinality:** The `interrupts` collector can produce many time series. Use filtering or aggregation in PromQL (e.g. `sum(rate(...)) by (instance)`) or limit retention if needed.

- **Related documentation:** See `docs/node-exporter-collectors-cluster-monitoring-config.md` for the cluster-monitoring-config reference and when to use this supplementary exporter.
