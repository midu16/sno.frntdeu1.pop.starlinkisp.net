#!/bin/bash
# Run scripts/sriov-nic-ids-eno2np1.sh on node master-0 via oc debug.
# oc debug does not attach local stdin to the pod, so we pass the script via base64.
#
# Usage: scripts/run-sriov-nic-ids-on-node.sh [interface]
#   Default interface: eno2np1
set -euo pipefail
SCRIPT_DIR="${SCRIPT_DIR:-$(dirname "$(readlink -f "$0")")}"
IFACE="${1:-eno2np1}"
B64=$(base64 < "$SCRIPT_DIR/sriov-nic-ids-eno2np1.sh" | tr -d '\n')
oc debug node/master-0 -- chroot /host bash -c 'echo "$1" | base64 -d | bash -s -- "$2"' _ "$B64" "$IFACE"
