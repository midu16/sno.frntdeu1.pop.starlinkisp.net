#!/usr/bin/env bash
# Apply abi-master-0/extra-manifests/operator-install in two phases with retries.
#
# Phase 1 (critical): LVMS/SR-IOV/PTP/logging/lightspeed — OwnNamespace-style
# operators that Day-2 config waits on.
# Phase 2 (remaining): GitOps/RHOAI/SNR (AllNamespaces) + MinIO (needs lvms-vg1).
#
# Prefer this over a single "oc apply -f operator-install/" so AllNamespaces
# CSV copies do not contend with LVMS/SR-IOV InstallPlan creation.
#
# Usage:
#   export KUBECONFIG=/path/to/kubeconfig
#   ./scripts/apply-operator-install.sh
#   ./scripts/apply-operator-install.sh --phase1-only
#   ./scripts/apply-operator-install.sh --phase2-only
set -euo pipefail

: "${KUBECONFIG:?Set KUBECONFIG}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="${ROOT}/abi-master-0/extra-manifests/operator-install"

if [[ ! -d "$DIR" ]]; then
  echo "Directory not found: $DIR" >&2
  exit 1
fi

PHASE1=(
  99_03_lvms.yaml
  99_05_sriov.yaml
  99_04_ptp.yaml
  99_02_logging.yaml
  99_08_lightspeed.yaml
)
PHASE2=(
  99_01_argo.yaml
  99_09_rhoai.yaml
  99_10_snr.yaml
  99_07_minio.yaml
  99_07_minio_routes.yaml
)

MODE="all"
case "${1:-}" in
  --phase1-only) MODE="phase1" ;;
  --phase2-only) MODE="phase2" ;;
  "") ;;
  *)
    echo "Usage: $0 [--phase1-only|--phase2-only]" >&2
    exit 2
    ;;
esac

apply_file() {
  local f="$1"
  local attempt
  for attempt in 1 2 3 4 5; do
    if oc apply -f "$f"; then
      return 0
    fi
    echo "Retry $attempt/5 for $f in $((attempt * 3))s ..." >&2
    sleep $((attempt * 3))
  done
  return 1
}

apply_list() {
  local label="$1"
  shift
  local name
  for name in "$@"; do
    local path="${DIR}/${name}"
    if [[ ! -f "$path" ]]; then
      echo "Missing: $path" >&2
      exit 1
    fi
    echo "=== ${label}: $(basename "$path") ==="
    apply_file "$path"
  done
}

if [[ "$MODE" == "all" || "$MODE" == "phase1" ]]; then
  apply_list "phase1" "${PHASE1[@]}"
fi
if [[ "$MODE" == "all" || "$MODE" == "phase2" ]]; then
  apply_list "phase2" "${PHASE2[@]}"
fi

echo "Done."
