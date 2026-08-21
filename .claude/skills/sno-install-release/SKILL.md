---
name: sno-install-release
description: Install or re-install the sno.frntdeu1.pop.starlinkisp.net SNO cluster on a specific OpenShift release image (GA/ec/rc or a CI nightly), driving the iDRAC via idrac_sushy.py. Use when the user asks to install/re-install/reprovision the SNO on a given release pullspec, RELEASE_IMAGE, ocp/release-5 nightly, or version, and especially when the payload is an unsigned CI nightly.
---

# Install SNO on a specific release image

End-to-end procedure to (re)install `sno.frntdeu1.pop.starlinkisp.net` (Dell R630, iDRAC)
on an arbitrary OpenShift release image. Covers the release-image override, the CI-nightly
pullspec gotcha, and the **unsigned-nightly signature-policy bake-in** that lets nightlies
install without any mid-install SSH remediation.

This is a **destructive, ~30–60 min** operation: `deploy` wipes and re-provisions the node.
The user asking to install on a release IS the authorization to proceed. The one decision
that changes the approach is signed-vs-unsigned payload (see §2).

## 0. Environment (this cluster)

| Parameter | Value |
|-----------|-------|
| Cluster / base domain | `sno` / `frntdeu1.pop.starlinkisp.net` |
| Node / rendezvous IP | `192.168.1.133` (SSH `core@192.168.1.133`) |
| iDRAC | `192.168.1.228`, user `root`, password via `IDRAC_PW` env |
| Webcache host | `rock@192.168.1.21:/apps/webcache/OSs/` → `http://192.168.1.21:8080/OSs/agent.x86_64.iso` |
| Registry auth / pull secret | `/home/midu/config.json` (SENSITIVE — never print/expose) |
| Python | `./.venv/bin/python3` (repo venv) |
| kubeconfig (post-install) | `workdir/auth/kubeconfig` |

## 1. Pick the driver: `--ocp-version` vs `--release-image`

- **Standard version** matching `X.Y.Z[-ec/fc/rc.N]` (e.g. `5.0.0-ec.6`, `4.18.6`) →
  drive with `--ocp-version` / `OCP_VERSION`. It maps to
  `quay.io/openshift-release-dev/ocp-release:<ver>-x86_64`.
- **CI nightly** (`5.0.0-0.nightly-…`) or any non-standard/mirrored image → you **must** use
  `--release-image` / `RELEASE_IMAGE`; the nightly version string does not match the
  installer's version regex.

> **Pullspec gotcha:** 5.x nightlies live in the **`ocp/release-5`** imagestream, *not*
> `ocp/release`. `registry.ci.openshift.org/ocp/release-5:5.0.0-0.nightly-…` is correct;
> `…/ocp/release:5.0.0-0.nightly-…` returns `manifest unknown`. Nightly *tags* are only fresh
> for a few days — record the by-digest form for durability.

Verify pullable + inspect the CMO/CCO commits (also tells you the node-exporter collector story):

```bash
IMG=registry.ci.openshift.org/ocp/release-5:5.0.0-0.nightly-YYYY-MM-DD-HHMMSS
oc adm release info "$IMG" -a /home/midu/config.json \
  -o jsonpath='{.image}{"\n"}{.metadata.version}{"\n"}'
oc adm release info "$IMG" -a /home/midu/config.json --commits \
  | grep -E 'cluster-monitoring-operator|cluster-config-operator'
```

## 2. Decide: signed payload or unsigned CI nightly?

- **GA / `ec` / `fc` / `rc` (`ocp-release`)** → signed. Keep the stock signed policy.
  **Do NOT** add the relaxed-policy MachineConfig.
- **CI nightly (`ocp-vX.Y-art-dev`)** → **unsigned**. RHCOS `/etc/containers/policy.json`
  requires `sigstoreSigned` for those scopes, so `machine-config-daemon-pull.service` blocks at
  firstboot (`"A signature was required, but no signature exists"`) and the install never
  completes.

**Fix for nightlies (proven, no manual step):** the repo ships
`abi-master-0/openshift/99-masters-relaxed-signature-policy.yaml`, a master MachineConfig that
writes an `insecureAcceptAnything` `policy.json`. The agent installer folds it into the master
**pivot ignition**, so firstboot starts with the 141-byte relaxed policy and the pull service
never blocks. Nothing to do if the file is already present (it is committed); just confirm it:

```bash
ls abi-master-0/openshift/99-masters-relaxed-signature-policy.yaml
```

If installing a **signed** payload, temporarily remove/rename that file before `prepare-configs`
so the node keeps the stock signed policy.

> If the MC is ever missing, its content mirrors `scripts/remediate-unsigned-nightly-policy.sh`:
> `{"default":[{"type":"insecureAcceptAnything"}],"transports":{"docker-daemon":{"":[{"type":"insecureAcceptAnything"}]}}}`
> as a base64 `data:` URL at `/etc/containers/policy.json`, ignition 3.2.0, mode 420, `overwrite: true`.

## 3. Install — step by step

Run the steps individually (the aggregate `preflight` gate fails only on `openssl`, which is
irrelevant when the password comes from `IDRAC_PW`). Do **not** echo `IDRAC_PW`.

```bash
cd /home/midu/sno.frntdeu1.pop.starlinkisp.net
export IMG=registry.ci.openshift.org/ocp/release-5:5.0.0-0.nightly-YYYY-MM-DD-HHMMSS
PY=./.venv/bin/python3

# 3.1 Extract the target installer (bakes the release digest into openshift-install)
$PY idrac_sushy.py --registry-auth /home/midu/config.json --release-image "$IMG" extract-installer
./openshift-install version            # confirm version + baked digest

# 3.2 Prepare configs (cleans workdir, templates pull secret, copies abi-master-0/openshift/*)
$PY idrac_sushy.py --registry-auth /home/midu/config.json prepare-configs
ls workdir/openshift/ | grep signature # confirm the policy MC staged (nightly only)

# 3.3 Build the agent ISO (folds openshift/ manifests into the ignition)
$PY idrac_sushy.py build-iso           # -> workdir/agent.x86_64.iso (~786 MB)

# 3.4 Copy the ISO to the webcache host
$PY idrac_sushy.py copy-iso

# 3.5 DESTRUCTIVE: iDRAC deploy — wipes & re-provisions the node
IDRAC_PW='<pw>' $PY idrac_sushy.py deploy http://192.168.1.21:8080/OSs/agent.x86_64.iso

# 3.6 Wait for install-complete (~90 min/attempt, gated on kube-apiserver /readyz)
IDRAC_PW='<pw>' $PY idrac_sushy.py wait-install
```

For a **standard version** instead of a nightly, swap the driver:
`make install IDRAC_PW='<pw>' OCP_VERSION=5.0.0-ec.6` (or `RELEASE_IMAGE=<pullspec>`).

Run `wait-install` in the background and monitor; it can take 30–60 min. Do not fabricate the
outcome — read the log/notification.

## 4. Monitor & verify (esp. the nightly firstboot)

Install phases in `wait-install.log`: cluster validated → `installing` → `Writing image to disk`
→ reboot/pivot (node ping drops) → firstboot → bootstrap complete → operators roll out.

**At the pivot window, confirm the nightly signature fix took (should NOT be stuck):**

```bash
ping -c1 -W2 192.168.1.133            # comes back after the pivot reboot
ssh -o StrictHostKeyChecking=no core@192.168.1.133 '
  sudo systemctl is-active machine-config-daemon-pull.service   # expect: active
  sudo stat -c%s /etc/containers/policy.json                    # expect: 141 (relaxed), not ~8060 (signed)
  sudo grep -o insecureAcceptAnything /etc/containers/policy.json'
```

If `pull-svc` is stuck (`activating`) and policy.json is ~8060 bytes, the bake-in did not apply
(e.g. signed-payload run, or MC missing) — fall back to the proven manual remediation:

```bash
scp scripts/remediate-unsigned-nightly-policy.sh core@192.168.1.133:/tmp/
ssh core@192.168.1.133 'sudo bash /tmp/remediate-unsigned-nightly-policy.sh'
```

## 5. Post-install health check

```bash
export KUBECONFIG=$(pwd)/workdir/auth/kubeconfig
oc get clusterversion         # VERSION == target, AVAILABLE=True, PROGRESSING=False
oc get nodes                  # master-0 Ready
oc get co | awk '$4=="True"||$5=="True"'   # (empty) no Progressing/Degraded operators
oc get mcp master             # UPDATED=True UPDATING=False DEGRADED=False
oc get mc 99-masters-relaxed-signature-policy   # present on nightly installs
```

Benign on nightlies: `clusterversion` `RetrievedUpdates=False … not found in the "stable-5.0"
channel`. Kubeadmin creds are in `workdir/auth/` and `workdir/wait-install.log` — do not print them.

## 6. Gotchas / lessons (proven 2026-08-21 on `5.0.0-0.nightly-2026-08-21-033959`)

- The bake-in works: firstboot had the 141-byte relaxed policy, pull service active, **no manual
  remediation and no MCP-degrade dance** (unlike a manual mid-install fix, which trips a transient
  `content mismatch for /etc/containers/policy.json` MCP degrade).
- `deploy` standalone needs the ISO URL positional arg; the aggregate `install` injects it.
- All 5.0.0 nightlies 08-14…08-21 pin CMO `a360f0b3` (softirqs wired; zoneinfo merge-bug;
  interrupts absent from the payload api) — see the repo's node-exporter validation docs.
- Never expose `/home/midu/config.json`, `IDRAC_PW`, or kubeadmin creds in output.
```
