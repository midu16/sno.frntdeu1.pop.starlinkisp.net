#!/bin/bash
# Output SR-IOV NIC info for a given interface in supported-nic-ids format:
#   "Vendor_Driver_Model: vendor_id pf_device_id vf_device_id"
#
# Runs inside `oc debug node/<node> -- chroot /host bash` — i.e. against the
# node's real sysfs (see sriov.RunOnNode, which base64-pipes this script).
set -euo pipefail
N="${1:-eno2np1}"
SYS="/sys/class/net/$N"
[ ! -d "$SYS" ] && echo "No interface $N" >&2 && exit 1

D=$(basename $(readlink -f "$SYS/device"))
DIR="/sys/bus/pci/devices/$D"
BDF="0000:$D"

# Vendor and device IDs (hex, strip 0x)
vid=$(printf '%x' $(cat "$DIR/vendor"))
did=$(printf '%x' $(cat "$DIR/device"))

# Resolve PF and VF device IDs
pf_did=""
vf_did=""
if [ -d "$DIR/physfn" ]; then
  P=$(basename $(readlink -f "$DIR/physfn"))
  pf_did=$(printf '%x' $(cat "/sys/bus/pci/devices/$P/device"))
  vf_did=$(printf '%x' $(cat "$DIR/device"))
elif [ -f "$DIR/sriov_totalvfs" ] && [ -d "$DIR/virtfn0" ]; then
  pf_did=$(printf '%x' $(cat "$DIR/device"))
  V=$(readlink "$DIR/virtfn0")
  vf_did=$(printf '%x' $(cat "$DIR/$V/device"))
else
  pf_did="$did"
  vf_did="$did"
fi

# Name from lspci (Vendor + Device) and driver, sanitized for ConfigMap key
vendor_str=""
device_str=""
if command -v lspci &>/dev/null; then
  while read -r line; do
    case "$line" in
      Vendor:*) vendor_str="${line#*:}"; vendor_str="${vendor_str# }"; vendor_str="${vendor_str#$'\t'}" ;;
      Device:*) device_str="${line#*:}"; device_str="${device_str# }"; device_str="${device_str#$'\t'}" ;;
    esac
  done < <(lspci -s "$D" -mm 2>/dev/null || true)
fi
driver_str=""
[ -d "$DIR/driver" ] && driver_str=$(basename $(readlink "$DIR/driver"))

# Build key: Vendor_Driver_Device (spaces -> underscores, collapse multiple _)
name_parts=("$vendor_str" "$driver_str" "$device_str")
name=""
for p in "${name_parts[@]}"; do
  [ -n "$p" ] && name="${name:+${name}_}$(echo "$p" | tr ' ' '_' | tr -s '_')"
done
[ -z "$name" ] && name="Unknown_${vid}_${pf_did}"

echo "${name}: ${vid} ${pf_did} ${vf_did}"
