# Node-exporter-zoneinfo (cluster Prometheus)

Supplementary node_exporter that exposes **zoneinfo**, **interrupts**, and **softirqs** (port 9101) when the cluster-monitoring-config does not support these collectors. Deploys into `openshift-monitoring`; **cluster Prometheus** scrapes it. The kustomization includes a ClusterRoleBinding that grants the privileged SCC to the default service account so the DaemonSet can use hostPID/hostPath/runAsUser=0.

## Apply

```bash
oc apply -k abi-master-0/extra-manifests/node-exporter-zoneinfo/
```

If the DaemonSet stays at 0 pods after the first apply, bump the `node-exporter-zoneinfo/force-rollout` annotation in **daemonset.yaml** (e.g. from `"1"` to `"2"`) and re-apply to force a rollout.

## Verify

- **Metrics:** `oc port-forward -n openshift-monitoring daemonset/node-exporter-zoneinfo 9101:9101` then `curl -s http://localhost:9101/metrics | grep -E 'node_zoneinfo_|node_intr_|node_softirqs'`
- **Prometheus:** Observe → Metrics, query e.g. `node_intr_total{job="node-exporter-zoneinfo"}` or `node_zoneinfo_managed_pages{job="node-exporter-zoneinfo"}`
