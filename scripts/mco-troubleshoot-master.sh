#!/usr/bin/env bash
# Machine Config Operator troubleshooting for SNO (single master node).
# Run on a host that can reach the API with a valid kubeconfig, e.g.:
#   export KUBECONFIG=/path/to/kubeconfig
#   ./scripts/mco-troubleshoot-master.sh
# Optional: RESTART_MCD=1 to delete the machine-config-daemon pod on master-0.
#
# If the pool is degraded due to stale rendered-master-* annotations or
# openshift-workload-pinning drift, run ./scripts/mco-recover-sno-master.sh
# (see script header for FIX_WORKLOAD_PINNING).

set -euo pipefail

: "${KUBECONFIG:?Set KUBECONFIG to a kubeconfig that authenticates to the cluster}"

NODE="${NODE:-master-0}"
NS=openshift-machine-config-operator

echo "=== oc whoami / cluster version ==="
oc whoami
oc version -o yaml 2>/dev/null | head -40 || true

echo ""
echo "=== Node annotations (machineconfiguration) ==="
if command -v jq >/dev/null 2>&1; then
  oc get node "$NODE" -o json | jq -r '.metadata.annotations // {} | to_entries[] | select(.key|startswith("machineconfiguration")) | "\(.key)=\(.value)"' 2>/dev/null || true
else
  oc get node "$NODE" -o jsonpath='{range $k, $v := .metadata.annotations}{printf "%s=%s\n" $k $v}{end}' 2>/dev/null | grep -E 'machineconfiguration\.openshift\.io/' || true
fi

echo ""
echo "=== machineconfigpool master ==="
oc get machineconfigpool master -o wide
oc describe machineconfigpool master | sed -n '1,80p'

echo ""
echo "=== clusteroperator machine-config ==="
oc get co machine-config -o wide
oc describe co machine-config | sed -n '1,120p'

echo ""
echo "=== machine-config-daemon pod on master ==="
oc get pods -n "$NS" -l k8s-app=machine-config-daemon -o wide --field-selector "spec.nodeName=$NODE"

echo ""
echo "=== machine-config-controller logs (tail) ==="
oc logs -n "$NS" deploy/machine-config-controller --tail=120 2>&1 || true

echo ""
echo "=== machine-config-daemon logs (tail) ==="
MCD_POD=$(oc get pods -n "$NS" -l k8s-app=machine-config-daemon -o jsonpath='{.items[0].metadata.name}' --field-selector "spec.nodeName=$NODE" 2>/dev/null || true)
if [[ -n "${MCD_POD:-}" ]]; then
  oc logs -n "$NS" "$MCD_POD" -c machine-config-daemon --tail=80 2>&1 || true
else
  echo "No machine-config-daemon pod found on $NODE"
fi

echo ""
echo "=== Node filesystem: /etc/machine-config-daemon (debug pod) ==="
oc debug "node/$NODE" -- chroot /host sh -c 'ls -la /etc/machine-config-daemon/ 2>&1; echo "---"; test -f /etc/machine-config-daemon/currentconfig && echo currentconfig:present || echo currentconfig:missing' 2>&1 || true

if [[ "${RESTART_MCD:-0}" == "1" ]]; then
  echo ""
  echo "=== RESTART_MCD=1: deleting machine-config-daemon pod on $NODE ==="
  oc delete pod -n "$NS" -l k8s-app=machine-config-daemon --field-selector "spec.nodeName=$NODE" --wait=true
  echo "Waiting 30s for new pod..."
  sleep 30
  oc get pods -n "$NS" -l k8s-app=machine-config-daemon -o wide --field-selector "spec.nodeName=$NODE"
  oc get co machine-config
fi

echo ""
echo "=== accelerated-container-startup (journal on node) ==="
oc debug "node/$NODE" -- chroot /host journalctl -u accelerated-container-startup.service -b --no-pager -n 30 2>&1 || true

echo ""
echo "Done."
