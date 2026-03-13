#!/bin/bash
# Run scripts/verify-sriov-vfs-eno2np1.sh on node master-0 via oc debug.
# Uses base64 to pass the script into the pod (oc debug does not attach stdin).
#
# Usage: scripts/run-verify-sriov-vfs-on-node.sh [interface]
#   Default interface: eno2np1
set -euo pipefail
SCRIPT_DIR="${SCRIPT_DIR:-$(dirname "$(readlink -f "$0")")}"
IFACE="${1:-eno2np1}"
B64=$(base64 < "$SCRIPT_DIR/verify-sriov-vfs-eno2np1.sh" | tr -d '\n')
oc debug node/master-0 -- bash -c 'echo "$1" | base64 -d | bash -s -- /host "$2"' _ "$B64" "$IFACE"
