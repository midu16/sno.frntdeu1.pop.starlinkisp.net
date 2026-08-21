#!/usr/bin/env bash
# Remediate an air-gapped/nightly SNO stuck at machine-config-daemon-pull.service with
#   "Source image rejected: A signature was required, but no signature exists"
# CI nightlies from quay.io/openshift-release-dev/ocp-vX.Y-art-dev are UNSIGNED, but the
# RHCOS policy.json requires sigstoreSigned for those scopes. Relax the signature policy
# both live (so the stuck podman pull + crio proceed) and inside the rendered
# machine-config content (so MCD firstboot / MCO do not revert it to the signed policy).
set -euo pipefail

POL=/etc/containers/policy.json
CONTENT=/etc/mcs-machine-config-content.json
TS="$(date +%s)"

echo "[*] Backing up policy.json and rendered machine-config content (suffix .bak.$TS)"
cp -a "$POL" "${POL}.bak.${TS}"
[ -f "$CONTENT" ] && cp -a "$CONTENT" "${CONTENT}.bak.${TS}"

cat > /tmp/relaxed-policy.json <<'JSON'
{
  "default": [{"type": "insecureAcceptAnything"}],
  "transports": {
    "docker-daemon": {"": [{"type": "insecureAcceptAnything"}]}
  }
}
JSON

echo "[*] Writing relaxed live policy.json"
cp /tmp/relaxed-policy.json "$POL"

if [ -f "$CONTENT" ]; then
  echo "[*] Patching embedded policy.json inside $CONTENT"
  python3 - "$CONTENT" /tmp/relaxed-policy.json <<'PY'
import json, base64, sys
content_path, pol_path = sys.argv[1], sys.argv[2]
b64 = base64.b64encode(open(pol_path, "rb").read()).decode()
src = "data:text/plain;charset=utf-8;base64," + b64
c = json.load(open(content_path))
files = c["spec"]["config"]["storage"]["files"]
n = 0
for f in files:
    if f.get("path") == "/etc/containers/policy.json":
        f["contents"]["source"] = src
        n += 1
json.dump(c, open(content_path, "w"))
print("    patched %d embedded policy.json entrie(s)" % n)
PY
fi

echo "[*] Restarting machine-config-daemon-pull.service"
systemctl restart machine-config-daemon-pull.service || true

echo "[*] Done. Watching pull service state for ~20s ..."
sleep 20
systemctl is-active machine-config-daemon-pull.service || true
