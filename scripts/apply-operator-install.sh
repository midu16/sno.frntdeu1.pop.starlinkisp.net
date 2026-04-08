#!/usr/bin/env bash
# Apply abi-master-0/extra-manifests/operator-install with oc apply (idempotent) and retries.
# Use this instead of a single "oc create -f ..." burst, which can trigger transient
# InternalError when the apiserver Service (172.30.0.1:443) is briefly unavailable under load.
#
# Usage:
#   export KUBECONFIG=/path/to/kubeconfig
#   ./scripts/apply-operator-install.sh
set -euo pipefail

: "${KUBECONFIG:?Set KUBECONFIG}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="${ROOT}/abi-master-0/extra-manifests/operator-install"

if [[ ! -d "$DIR" ]]; then
  echo "Directory not found: $DIR" >&2
  exit 1
fi

shopt -s nullglob
mapfile -t FILES < <(printf '%s\n' "$DIR"/*.yaml | sort)

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "No yaml files in $DIR" >&2
  exit 1
fi

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

for f in "${FILES[@]}"; do
  echo "=== $(basename "$f") ==="
  apply_file "$f"
done

echo "Done."
