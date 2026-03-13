#!/bin/bash
# Apply test-apps/sriov and verify a pod has SR-IOV VF(s) attached.
# Usage: ./validate-vf-attachment.sh [netdev-only]
#   netdev-only: deploy only netdevice (no DPDK/hugepages); default is full deployment.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NETDEV_ONLY="${1:-}"

cd "$REPO_ROOT"

echo "=== 1. Apply test-apps/sriov (kustomize) ==="
oc apply -k test-apps/sriov/

echo "=== 2. Grant privileged SCC to default SA in sriov-test-ns ==="
oc adm policy add-scc-to-user privileged -z default -n sriov-test-ns 2>/dev/null || true

if [[ -n "$NETDEV_ONLY" ]]; then
  echo "=== 3. Apply netdevice-only deployment (no DPDK/hugepages) ==="
  oc apply -f test-apps/sriov/sriov-test-deployment-netdev-only.yaml
  DEPLOY="sriov-test-netdev-only"
else
  DEPLOY="sriov-test"
fi

echo "=== 4. Wait for pod Running (deployment/$DEPLOY) ==="
oc wait -n sriov-test-ns "deployment/$DEPLOY" --for=condition=Available --timeout=120s || {
  echo "Deployment did not become Available. Check: oc get pods -n sriov-test-ns; oc describe pod -n sriov-test-ns -l app=$DEPLOY"
  exit 1
}

POD=$(oc get pods -n sriov-test-ns -l "app=$DEPLOY" -o jsonpath='{.items[0].metadata.name}')
echo "Pod: $POD"

echo "=== 5. Verify VF attachment (interfaces in pod) ==="
oc exec -n sriov-test-ns "$POD" -- ip -br link show
echo "---"
oc exec -n sriov-test-ns "$POD" -- ip -br addr show

echo "=== Done: VF(s) attached to pod $POD ==="
