#!/usr/bin/env bash
# Recover MachineConfigPool master when:
#   - Node machineconfiguration annotations reference a non-existent rendered-master-* (NotFound), or
#   - MCD degrades on /etc/kubernetes/openshift-workload-pinning vs rendered MC.
#
# Requires: oc, jq; base64 on PATH (GNU base64 -w0 or BSD base64).
# Usage:
#   export KUBECONFIG=.../auth/kubeconfig
#   ./scripts/mco-recover-sno-master.sh
#
# Optional:
#   FIX_WORKLOAD_PINNING=1  — always rewrite openshift-workload-pinning from the pool's rendered MC
#                           (even if the node reason text does not mention it).
set -euo pipefail

: "${KUBECONFIG:?Set KUBECONFIG}"

NODE="${NODE:-master-0}"
NS=openshift-machine-config-operator
FIX_WLP="${FIX_WORKLOAD_PINNING:-0}"

RENDERED=$(oc get machineconfigpool master -o jsonpath='{.spec.configuration.name}')
if [[ -z "$RENDERED" || "$RENDERED" == null ]]; then
  echo "Could not read spec.configuration.name from machineconfigpool master" >&2
  exit 1
fi

if ! oc get machineconfig "$RENDERED" &>/dev/null; then
  echo "Rendered MachineConfig ${RENDERED} not found in API; fix manifests/MCP first." >&2
  exit 1
fi

CUR=$(oc get node "$NODE" -o jsonpath="{.metadata.annotations.machineconfiguration\.openshift\.io/currentConfig}" 2>/dev/null || true)
DES=$(oc get node "$NODE" -o jsonpath="{.metadata.annotations.machineconfiguration\.openshift\.io/desiredConfig}" 2>/dev/null || true)
REASON=$(oc get node "$NODE" -o jsonpath="{.metadata.annotations.machineconfiguration\.openshift\.io/reason}" 2>/dev/null || true)

need_annotate=0
if [[ "$CUR" != "$RENDERED" ]] || [[ "$DES" != "$RENDERED" ]]; then
  need_annotate=1
fi
if [[ -n "$CUR" ]] && ! oc get machineconfig "$CUR" &>/dev/null; then
  echo "Node references missing MachineConfig: currentConfig=${CUR}"
  need_annotate=1
fi

if [[ "$need_annotate" == "1" ]]; then
  echo "Aligning node ${NODE} annotations to pool rendered config: ${RENDERED}"
  oc annotate node "$NODE" \
    machineconfiguration.openshift.io/currentConfig="${RENDERED}" \
    machineconfiguration.openshift.io/desiredConfig="${RENDERED}" \
    --overwrite
fi

sync_workload_pinning() {
  local mc="$1"
  local tmp b64
  tmp=$(mktemp)
  oc get machineconfig "$mc" -o json | jq -r \
    '.spec.config.storage.files[]? | select(.path=="/etc/kubernetes/openshift-workload-pinning") | .contents.source' \
    | sed 's/^data:text\/plain;charset=utf-8;base64,//' | base64 -d >"$tmp"
  b64=$(base64 -w0 "$tmp" 2>/dev/null || base64 "$tmp" | tr -d '\n')
  rm -f "$tmp"
  echo "Rewriting /etc/kubernetes/openshift-workload-pinning on ${NODE} from ${mc}"
  oc debug "node/$NODE" -- chroot /host sh -c "echo $b64 | base64 -d > /etc/kubernetes/openshift-workload-pinning && chmod 644 /etc/kubernetes/openshift-workload-pinning"
}

has_wlp_in_mc() {
  oc get machineconfig "$1" -o json | jq -e '.spec.config.storage.files[]? | select(.path=="/etc/kubernetes/openshift-workload-pinning")' &>/dev/null
}

wlp_fix=0
[[ "$FIX_WLP" == "1" ]] && wlp_fix=1
[[ "$REASON" == *"openshift-workload-pinning"* ]] && wlp_fix=1
[[ "$REASON" == *"current config on disk during bootstrap"* ]] && wlp_fix=1

if has_wlp_in_mc "$RENDERED" && [[ "$wlp_fix" == "1" ]]; then
  sync_workload_pinning "$RENDERED"
fi

echo "Restarting machine-config-daemon on ${NODE}"
oc delete pod -n "$NS" -l k8s-app=machine-config-daemon --field-selector "spec.nodeName=$NODE" --wait=true
sleep 15
oc get co machine-config
oc get machineconfigpool master
echo "Done. If still degraded: oc logs -n $NS -l k8s-app=machine-config-daemon -c machine-config-daemon --tail=80"
