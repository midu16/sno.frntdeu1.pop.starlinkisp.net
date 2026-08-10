#!/usr/bin/env bash
# Collect openshift-install / ABI failure context for CI (runs best-effort).
# Writes to abi-install-diagnostics/ in workspace root — suitable as a workflow artifact.
set +e
ROOT="${GITHUB_WORKSPACE:-$(pwd)}"
OUT="$ROOT/abi-install-diagnostics"
WORKDIR="${WORKDIR_OVERRIDE:-$ROOT/workdir}"
CLUSTER_IP="${CLUSTER_IP:-192.168.1.133}"
API_HOST="${API_HOST:-api.sno.frntdeu1.pop.starlinkisp.net}"
REMOTE_USER="${REMOTE_USER:-rock}"
REMOTE_HOST="${REMOTE_HOST:-192.168.1.21}"
mkdir -p "$OUT"

echo "collect_abi_install_diagnostics: OUT=$OUT WORKDIR=$WORKDIR CLUSTER_IP=$CLUSTER_IP" | tee "$OUT/meta.txt"
date -u | tee -a "$OUT/meta.txt"

# Common installer bookkeeping files at workdir root
for f in .openshift_install.log .openshift_install_state.json; do
  if [[ -f "${WORKDIR}/${f}" ]]; then
    cp -a "${WORKDIR}/${f}" "$OUT/" 2>/dev/null || true
  fi
done

# Any loose log files beside workdir
shopt -s nullglob
for pat in "${WORKDIR}"/*.log; do
  [[ -f "$pat" ]] && cp -a "$pat" "$OUT/" 2>/dev/null || true
done
shopt -u nullglob

# Connectivity during SNO MCO reboot: no route to host → connection refused → ready
{
  echo "=== date ==="
  date -u
  echo "=== ping ${CLUSTER_IP} ==="
  ping -c 5 -W 2 "${CLUSTER_IP}" 2>&1 || true
  echo "=== tcp ${CLUSTER_IP}:6443 ==="
  if command -v nc >/dev/null 2>&1; then
    nc -zv -w 3 "${CLUSTER_IP}" 6443 2>&1 || true
  else
    timeout 3 bash -c "echo >/dev/tcp/${CLUSTER_IP}/6443" 2>&1 && echo open || echo closed
  fi
  echo "=== readyz https://${CLUSTER_IP}:6443/readyz ==="
  curl -sk --connect-timeout 5 -m 10 -w "\nhttp_code=%{http_code} time=%{time_total}\n" \
    "https://${CLUSTER_IP}:6443/readyz" 2>&1 || true
  echo "=== readyz https://${API_HOST}:6443/readyz ==="
  curl -sk --connect-timeout 5 -m 10 -w "\nhttp_code=%{http_code} time=%{time_total}\n" \
    "https://${API_HOST}:6443/readyz" 2>&1 || true
  echo "=== getent ${API_HOST} ==="
  getent hosts "${API_HOST}" 2>&1 || true
  echo "=== ip route get ${CLUSTER_IP} ==="
  ip route get "${CLUSTER_IP}" 2>&1 || true
} >"$OUT/api-connectivity.txt" 2>&1

# Best-effort node view via webcache host (SSH key from ensure-ssh-key)
if command -v ssh >/dev/null 2>&1; then
  {
    echo "=== ${REMOTE_USER}@${REMOTE_HOST} → core@${CLUSTER_IP} ==="
    ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 \
      "${REMOTE_USER}@${REMOTE_HOST}" \
      "ping -c 3 -W 2 ${CLUSTER_IP}; echo ---; \
       ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 \
         core@${CLUSTER_IP} 'uptime; systemctl is-active kubelet crio; \
         curl -sk --connect-timeout 2 https://127.0.0.1:6443/readyz; echo; \
         last -x reboot | head -5' 2>&1" 2>&1 || true
  } >"$OUT/node-via-webcache.txt" 2>&1
fi

# Grep installer log for API outage markers (SNO reboot signature)
if [[ -f "${WORKDIR}/.openshift_install.log" ]]; then
  {
    echo "=== API / dial outage lines (tail) ==="
    grep -E -i 'no route to host|connection refused|i/o timeout|Failed to watch|dial tcp|server is down|cluster to initialize|Cluster initialization failed' \
      "${WORKDIR}/.openshift_install.log" | tail -n 80 || true
  } >"$OUT/install-log-api-outage.txt" 2>&1
fi

KCFG="${WORKDIR}/auth/kubeconfig"
if [[ -f "$KCFG" ]]; then
  export KUBECONFIG="$KCFG"
  {
    command -v oc >/dev/null 2>&1 && oc version -o yaml
  } >"$OUT/oc-version.yaml" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc get nodes -o wide
  } >"$OUT/nodes.txt" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc get clusteroperators -o wide
  } >"$OUT/clusteroperators.txt" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc get clusteroperators -o yaml
  } >"$OUT/clusteroperators.yaml" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc describe machineconfigpool master
  } >"$OUT/mcp-master-describe.txt" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc get machineconfigpool -o yaml
  } >"$OUT/machineconfigpools.yaml" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc get nodes -o yaml
  } >"$OUT/nodes.yaml" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc get pods -n openshift-machine-config-operator -o wide
  } >"$OUT/mco-pods.txt" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc logs -n openshift-machine-config-operator \
      -l controller=machine-config-controller --tail=800
  } >"$OUT/mcc-logs.txt" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc get events -A --sort-by='.lastTimestamp' | tail -200
  } >"$OUT/events-recent.txt" 2>&1 || true
  {
    command -v oc >/dev/null 2>&1 && oc get clusterversion -o yaml
  } >"$OUT/clusterversion.yaml" 2>&1 || true
else
  echo "No kubeconfig at $KCFG — skipping oc collection" | tee "$OUT/oc-skipped.txt"
fi

echo "Done. Directory: $OUT" | tee -a "$OUT/meta.txt"
exit 0
