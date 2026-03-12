# SRIOV NIC discovery (oc debug node/master-0)

Findings from `oc debug node/master-0 -- chroot /host` on the cluster.

## Physical network interfaces (host)

| Interface | State | PCI address   | Driver  | Vendor:Device | Model |
|-----------|--------|---------------|---------|----------------|-------|
| eno1np0   | UP     | 0000:01:00.0  | bnxt_en | 14e4:16d8      | Broadcom BCM57416 NetXtreme-E 10G RDMA |
| eno2np1   | DOWN   | 0000:01:00.1  | bnxt_en | 14e4:16d8      | Broadcom BCM57416 NetXtreme-E 10G RDMA |
| eno3      | DOWN   | 0000:06:00.0  | tg3     | 14e4:165f      | Broadcom NetXtreme BCM5720 Gigabit |
| eno4      | DOWN   | 0000:06:00.1  | tg3     | 14e4:165f      | Broadcom NetXtreme BCM5720 Gigabit |

## SRIOV capability (sysfs)

- **eno2np1** is the interface name used in your policies (`sriov-config-netdevice-eno2np1`, `sriov-config-dpdk-eno2np1`). It exists on the node and is backed by **0000:01:00.1** (Broadcom BCM57416).
- For all four NICs, `/sys/bus/pci/devices/0000:<slot>/sriov_totalvfs` **does not exist**. So either:
  - SRIOV is not supported by this hardware/firmware, or
  - SRIOV is disabled (e.g. in BIOS), or
  - The driver does not expose SRIOV in sysfs.
- OpenShift SRIOV Network Operator supports **Intel** (8086) and **Mellanox** (15b3) only; it reports **Broadcom (14e4)** as "unsupported model", so it will not configure VFs on these NICs even if they were SRIOV-capable.

## Conclusion: “right NIC” on this node

- **By name:** The right NIC for your current manifests is **eno2np1** (it exists; policies already reference it).
- **By operator support:** On this node there is **no NIC that the operator will use**. All physical NICs are Broadcom; the operator does not support Broadcom and no device has `sriov_totalvfs` in sysfs.
- To get SRIOV working you need a node that has an **Intel or Mellanox** SRIOV-capable NIC and, if you keep the same policy names, either:
  - name the PF **eno2np1** (or whatever name you put in `nicSelector.pfNames`), or
  - change the policies’ `nicSelector` (e.g. `pfNames` or vendor/device ID) to match that node’s PF.

## Commands used (for re-run or other nodes)

```bash
export KUBECONFIG=/path/to/kubeconfig

# List PCI network devices
oc debug node/master-0 -- chroot /host lspci -nn -d 0200:

# List host interfaces
oc debug node/master-0 -- chroot /host ip -br link show

# Map PCI to interface and check SRIOV
oc debug node/master-0 -- chroot /host sh -c '
  for dev in 01:00.0 01:00.1 06:00.0 06:00.1; do
    echo "=== 0000:$dev ==="
    [ -f /sys/bus/pci/devices/0000:$dev/sriov_totalvfs ] && echo sriov_totalvfs: $(cat /sys/bus/pci/devices/0000:$dev/sriov_totalvfs) || echo no sriov_totalvfs
    [ -d /sys/bus/pci/devices/0000:$dev/net ] && ls /sys/bus/pci/devices/0000:$dev/net/
  done
'

# Driver and vendor/device
oc debug node/master-0 -- chroot /host sh -c '
  for dev in 01:00.0 01:00.1 06:00.0 06:00.1; do
    d=/sys/bus/pci/devices/0000:$dev
    echo "$dev: driver=$(readlink -f $d/driver 2>/dev/null | xargs basename) vendor=$(cat $d/vendor 2>/dev/null) device=$(cat $d/device 2>/dev/null)"
  done
'
```
