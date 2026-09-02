#!/bin/bash
# Verify that SR-IOV VFs have been created on a given interface (default eno2np1).
# Checks sriov_numvfs and presence of virtfn* under the PF's device sysfs.
#
# Invoked from the `oc debug` pod (host sysfs via /host): receives "/host"
# as its first argument (SYSROOT), then the interface name.
set -euo pipefail

SYSROOT=""
IFACE="eno2np1"
if [[ "${1:-}" == /host ]]; then
  SYSROOT="/host"
  shift
fi
[[ -n "${1:-}" ]] && IFACE="$1"

SYS_NET="${SYSROOT}/sys/class/net/${IFACE}"
DEVICE="${SYS_NET}/device"

if [[ ! -d "$SYS_NET" ]]; then
  echo "Error: interface ${IFACE} not found (no ${SYS_NET})"
  exit 1
fi

if [[ ! -d "$DEVICE" ]]; then
  echo "Error: no device directory for ${IFACE} (${DEVICE})"
  exit 1
fi

# sriov_numvfs: current number of VFs (written by driver/operator)
NUMVFS_FILE="${DEVICE}/sriov_numvfs"
# sriov_totalvfs: max VFs supported by the PF
TOTALVFS_FILE="${DEVICE}/sriov_totalvfs"

if [[ ! -f "$NUMVFS_FILE" ]]; then
  echo "Error: ${NUMVFS_FILE} not found."
  echo "SR-IOV may not be enabled for ${IFACE} (driver/firmware or VFs not yet created)."
  exit 1
fi

NUMVFS=$(cat "$NUMVFS_FILE")
TOTALVFS=""
[[ -f "$TOTALVFS_FILE" ]] && TOTALVFS=$(cat "$TOTALVFS_FILE")

if [[ ! -d "${DEVICE}/virtfn0" ]]; then
  echo "Error: no virtfn0 under ${DEVICE} (VFs not created)."
  echo "  ${IFACE} sriov_numvfs=${NUMVFS} sriov_totalvfs=${TOTALVFS:-N/A}"
  exit 1
fi

VF_COUNT=$(find "$DEVICE" -maxdepth 1 -name 'virtfn*' 2>/dev/null | wc -l)
echo "OK: VFs created on ${IFACE}"
echo "  sriov_numvfs:    ${NUMVFS}"
echo "  sriov_totalvfs:  ${TOTALVFS:-N/A}"
echo "  virtfn count:    ${VF_COUNT}"
exit 0
