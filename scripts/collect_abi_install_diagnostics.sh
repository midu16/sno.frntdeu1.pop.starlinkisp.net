#!/usr/bin/env bash
# Collect openshift-install / ABI failure context for CI (runs best-effort).
# Writes to abi-install-diagnostics/ in workspace root — suitable as a workflow artifact.
set +e
ROOT="${GITHUB_WORKSPACE:-$(pwd)}"
OUT="$ROOT/abi-install-diagnostics"
WORKDIR="${WORKDIR_OVERRIDE:-$ROOT/workdir}"
mkdir -p "$OUT"

echo "collect_abi_install_diagnostics: OUT=$OUT WORKDIR=$WORKDIR" | tee "$OUT/meta.txt"

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
else
  echo "No kubeconfig at $KCFG — skipping oc collection" | tee "$OUT/oc-skipped.txt"
fi

echo "Done. Directory: $OUT" | tee -a "$OUT/meta.txt"
exit 0
