#!/usr/bin/env python3
"""SNO OpenShift installer with iDRAC management via sushy (Redfish).

Complete end-to-end installer: preflight checks, client extraction,
config templating, ISO build, ISO transfer, iDRAC boot management,
and install-complete monitoring.

Requires: pip install sushy sushy-oem-idrac

Subcommands:
  preflight          Check/install all prerequisites
  ensure-ssh-key     Generate SSH key if missing, copy to webcache host
  extract-installer  Extract openshift-install from OCP release image
  prepare-configs    Prepare workdir with templated install-config + agent-config
  build-iso          Build agent ISO via openshift-install
  copy-iso           SCP agent ISO to the webcache host
  status             Show iDRAC system model, power state, virtual media
  eject              Eject virtual media from VirtualCD slot
  insert             Insert an ISO via HTTP URL into VirtualCD slot
  set-boot-cd        Set one-time boot to VirtualCD (Dell OEM)
  set-boot-hdd       Set one-time boot to HDD (Dell OEM)
  restart            Force-restart the server
  power-on           Power on the server
  power-off          Force power off the server
  wait-power-on      Poll until the server reaches powered-on state
  deploy             iDRAC full cycle: eject → insert → boot-cd → restart → wait
  wait-install       Wait for openshift-install agent install-complete
  remediate-mco      Fix stuck machine-config (annotations, CSRs, MCD restart)
  install            Full end-to-end SNO installation (all steps above)

API readiness: between install-complete retries the installer waits for kube-apiserver
/readyz (env API_READY_WAIT_SEC / API_READY_SETTLE_SEC) so SNO MCO reboots that
produce "no route to host" / "connection refused" do not immediately burn another
~40m wait-for window.
"""

import argparse
import base64
import getpass
import json
import os
import re
import shutil
import socket
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from subprocess import CalledProcessError
from urllib.parse import urlparse

SEPARATOR = "=" * 92

DEFAULTS = {
    "workdir": "./workdir",
    "src_dir": "./abi-master-0",
    "installer": "./openshift-install",
    "ocp_version": "4.22.0-ec.3",
    "idrac_ip": "192.168.1.228",
    "idrac_user": "root",
    "remote_user": "rock",
    "remote_host": "192.168.1.21",
    "remote_path": "/apps/webcache/OSs/",
    # SNO node / rendezvous IP — used for API readiness probes when kubeconfig is absent
    "cluster_ip": "192.168.1.133",
    "ssh_key": str(Path.home() / ".ssh" / "id_ed25519.pub"),
    "registry_auth": str(Path.home() / ".docker" / "config.json"),
}

_API_SERVER_RE = re.compile(r"(?m)^\s*server:\s*(\S+)\s*$")
_RENDEZVOUS_IP_RE = re.compile(r"(?m)^\s*rendezvousIP:\s*(\S+)\s*$")
_API_OUTAGE_MARKERS = (
    "no route to host",
    "connection refused",
    "i/o timeout",
    "network is unreachable",
    "connection reset by peer",
    "tls: internal error",
    "server is currently unable to handle the request",
    "dial tcp",
)


def _default_install_wait_attempts():
    """openshift-install agent wait-for install-complete allows ~90m per invocation."""
    raw = os.environ.get("INSTALL_WAIT_ATTEMPTS", "2")
    try:
        return max(1, int(raw))
    except ValueError:
        return 2


def _default_remediation_install_wait_attempts():
    """Extra wait-for rounds after primary waits fail (e.g. MCO reconciling slowly).

    Env REMEDIATION_INSTALL_WAIT_ATTEMPTS (non-negative integer). Default 0 = disabled.
    """
    raw = os.environ.get("REMEDIATION_INSTALL_WAIT_ATTEMPTS", "0")
    try:
        return max(0, int(raw))
    except ValueError:
        return 0


def _remediation_install_attempts(args):
    v = getattr(args, "remediation_install_wait_attempts", None)
    if v is not None:
        return max(0, int(v))
    return _default_remediation_install_wait_attempts()


def _skip_mc_remediation(args) -> bool:
    if args is not None and getattr(args, "skip_mc_remediation", False):
        return True
    return os.environ.get("SKIP_MC_REMEDIATION", "").strip().lower() in ("1", "true", "yes")


def _mc_remediation_wait_sec(args) -> int:
    if args is not None:
        v = getattr(args, "mc_remediation_wait_sec", None)
        if v is not None:
            return max(0, int(v))
    raw = os.environ.get("MC_REMEDIATION_WAIT_SEC", "120")
    try:
        return max(0, int(raw))
    except ValueError:
        return 120


def _env_int(name: str, default: int, minimum: int = 0) -> int:
    raw = os.environ.get(name, str(default))
    try:
        return max(minimum, int(raw))
    except ValueError:
        return default


def _api_ready_wait_sec(args=None) -> int:
    if args is not None:
        v = getattr(args, "api_ready_wait_sec", None)
        if v is not None:
            return max(0, int(v))
    # SNO MCO reboot + kube-apiserver bring-up often needs 10–20+ minutes.
    return _env_int("API_READY_WAIT_SEC", 1800)


def _api_ready_poll_sec(args=None) -> int:
    if args is not None:
        v = getattr(args, "api_ready_poll_sec", None)
        if v is not None:
            return max(1, int(v))
    return _env_int("API_READY_POLL_SEC", 15, minimum=1)


def _api_ready_settle_sec(args=None) -> int:
    if args is not None:
        v = getattr(args, "api_ready_settle_sec", None)
        if v is not None:
            return max(0, int(v))
    # After readyz flips to ok, give control-plane a short settle before
    # restarting openshift-install's ~40m cluster-init timer.
    return _env_int("API_READY_SETTLE_SEC", 90)


def _api_ready_stable_polls(args=None) -> int:
    if args is not None:
        v = getattr(args, "api_ready_stable_polls", None)
        if v is not None:
            return max(1, int(v))
    return _env_int("API_READY_STABLE_POLLS", 3, minimum=1)


def _kubeconfig_api_server(kubeconfig_path) -> str | None:
    path = Path(kubeconfig_path)
    if not path.is_file():
        return None
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None
    match = _API_SERVER_RE.search(text)
    if not match:
        return None
    return match.group(1).rstrip("/")


def _rendezvous_ip_from_workdir(workdir) -> str | None:
    agent = Path(workdir) / "agent-config.yaml"
    if not agent.is_file():
        return None
    try:
        text = agent.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None
    match = _RENDEZVOUS_IP_RE.search(text)
    if not match:
        return None
    return match.group(1).strip().strip("\"'")


def _api_probe_targets(kubeconfig_path, workdir=None, args=None) -> list[str]:
    """HTTPS API base URLs to probe (kubeconfig server first, then node IP)."""
    targets = []
    server = _kubeconfig_api_server(kubeconfig_path)
    if server:
        targets.append(server)
    cluster_ip = None
    if workdir:
        cluster_ip = _rendezvous_ip_from_workdir(workdir)
    if not cluster_ip and args is not None:
        cluster_ip = getattr(args, "cluster_ip", None) or DEFAULTS.get("cluster_ip")
    if not cluster_ip:
        cluster_ip = DEFAULTS.get("cluster_ip")
    if cluster_ip:
        ip_url = f"https://{cluster_ip}:6443"
        if ip_url not in targets:
            targets.append(ip_url)
    return targets


def _tcp_connect_ok(host: str, port: int, timeout: float = 5.0) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def _api_readyz_ok(server_url: str, timeout: float = 10.0) -> bool:
    """Return True when kube-apiserver /readyz responds OK (TLS verify skipped)."""
    url = server_url.rstrip("/") + "/readyz"
    ctx = ssl._create_unverified_context()
    try:
        with urllib.request.urlopen(url, context=ctx, timeout=timeout) as resp:
            code = getattr(resp, "status", resp.getcode())
            body = resp.read().decode("utf-8", errors="replace").strip().lower()
            return code == 200 and (body == "ok" or body.startswith("ok"))
    except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, OSError):
        return False


def _any_api_ready(targets: list[str]) -> tuple[bool, str | None]:
    for target in targets:
        parsed = urlparse(target)
        host = parsed.hostname
        port = parsed.port or 6443
        if host and not _tcp_connect_ok(host, port):
            continue
        if _api_readyz_ok(target):
            return True, target
    return False, None


def _install_log_suggests_api_outage(workdir) -> bool:
    log = Path(workdir) / ".openshift_install.log"
    if not log.is_file():
        return False
    try:
        # Tail last ~64KiB — enough for recent reflector / dial errors.
        with log.open("rb") as fh:
            fh.seek(0, os.SEEK_END)
            size = fh.tell()
            fh.seek(max(0, size - 65536), os.SEEK_SET)
            tail = fh.read().decode("utf-8", errors="replace").lower()
    except OSError:
        return False
    return any(marker in tail for marker in _API_OUTAGE_MARKERS)


def wait_for_api_ready(kubeconfig_path, workdir=None, args=None, *, label="API") -> bool:
    """Block until kube-apiserver /readyz is stably reachable.

    Used between install-complete retries so a SNO MCO reboot (no route to host /
    connection refused) does not immediately burn another ~40m wait-for window.

    Returns True if ready (or wait disabled); False if timeout exhausted.
    """
    timeout = _api_ready_wait_sec(args)
    if timeout <= 0:
        print(f"  Skipping {label} readiness wait (API_READY_WAIT_SEC=0).", flush=True)
        return True

    poll = _api_ready_poll_sec(args)
    settle = _api_ready_settle_sec(args)
    stable_needed = _api_ready_stable_polls(args)
    targets = _api_probe_targets(kubeconfig_path, workdir=workdir, args=args)
    if not targets:
        print(f"  WARNING: no {label} probe targets; skipping readiness wait.", flush=True)
        return True

    print(
        f"Waiting up to {timeout}s for {label} readiness "
        f"(poll={poll}s settle={settle}s stable={stable_needed}; targets={', '.join(targets)}) ...",
        flush=True,
    )
    deadline = time.monotonic() + timeout
    stable = 0
    last_ok = None
    while time.monotonic() < deadline:
        ok, which = _any_api_ready(targets)
        if ok:
            stable += 1
            last_ok = which
            print(f"  [{stable}/{stable_needed}] {label} ready via {which}", flush=True)
            if stable >= stable_needed:
                if settle > 0:
                    print(
                        f"  {label} stable; settling {settle}s before continuing "
                        "(avoids restarting wait-for during post-reboot flap) ...",
                        flush=True,
                    )
                    time.sleep(settle)
                print(f"  {label} readiness gate passed ({last_ok}).", flush=True)
                return True
        else:
            if stable:
                print(
                    f"  {label} became unreachable again after {stable} ok poll(s); "
                    "resetting stability counter (likely node reboot / apiserver restart).",
                    flush=True,
                )
            stable = 0
            remaining = int(max(0, deadline - time.monotonic()))
            print(f"  {label} not ready yet ({remaining}s left) ...", flush=True)
        time.sleep(poll)

    print(f"  WARNING: timed out waiting for {label} readiness.", flush=True)
    return False


_MCO_NS = "openshift-machine-config-operator"
_MCP_MASTER = "master"
_MCD_LABEL = "k8s-app=machine-config-daemon"
_MCD_ISSUE_MARKERS = (
    "machine-config-daemon/currentconfig",
    "/etc/machine-config-daemon/currentconfig",
    "currentconfig: no such file",
    "MachineConfigPool master is not ready",
    "syncRequiredMachineConfigPools",
    "current config on disk during bootstrap",
)
_NODE_REPORTING_RE = re.compile(
    r"Node\s+([a-zA-Z0-9.-]+)\s+is\s+reporting",
    re.IGNORECASE,
)
_WLP_PATH = "/etc/kubernetes/openshift-workload-pinning"


def _oc_env(kubeconfig: str) -> dict:
    env = os.environ.copy()
    env["KUBECONFIG"] = kubeconfig
    return env


def _oc_run(kubeconfig, *oc_args, check=False, capture=True, timeout=None):
    cmd = ["oc", *oc_args]
    print(f"  > {' '.join(cmd)}")
    return subprocess.run(
        cmd,
        env=_oc_env(kubeconfig),
        check=check,
        capture_output=capture,
        text=True,
        timeout=timeout,
    )


def _oc_json(kubeconfig, *oc_args):
    result = _oc_run(kubeconfig, *oc_args, "-o", "json")
    if result.returncode != 0:
        return None
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        return None


def _machine_config_diagnostic_text(kubeconfig) -> str:
    parts = []
    co = _oc_json(kubeconfig, "get", "clusteroperator", "machine-config")
    if co:
        for cond in co.get("status", {}).get("conditions", []):
            parts.append(str(cond.get("message", "")))
            parts.append(str(cond.get("reason", "")))
    mcp = _oc_json(kubeconfig, "get", "machineconfigpool", _MCP_MASTER)
    if mcp:
        for cond in mcp.get("status", {}).get("conditions", []):
            parts.append(str(cond.get("message", "")))
            parts.append(str(cond.get("reason", "")))
    return "\n".join(parts)


def _matches_mcd_issue(text: str) -> bool:
    t = text.lower()
    if "currentconfig" in t and "no such file" in t:
        return True
    if "bootstrap" in t and "currentconfig" in t:
        return True
    for marker in _MCD_ISSUE_MARKERS:
        if marker.lower() in t:
            return True
    return False


def _co_machine_config_degraded(kubeconfig) -> bool:
    co = _oc_json(kubeconfig, "get", "clusteroperator", "machine-config")
    if not co:
        return False
    for cond in co.get("status", {}).get("conditions", []):
        if cond.get("type") == "Degraded" and cond.get("status") == "True":
            return True
    return False


def _mcp_master_needs_work(kubeconfig) -> bool:
    mcp = _oc_json(kubeconfig, "get", "machineconfigpool", _MCP_MASTER)
    if not mcp:
        return False
    st = mcp.get("status") or {}
    for key in ("unavailableMachineCount", "degradedMachineCount"):
        val = st.get(key)
        if val not in (None, 0):
            return True
    for cond in st.get("conditions", []):
        if cond.get("type") == "Degraded" and cond.get("status") == "True":
            return True
        if cond.get("type") == "Updated" and cond.get("status") != "True":
            return True
    return False


def _mcp_master_updated(kubeconfig) -> bool:
    mcp = _oc_json(kubeconfig, "get", "machineconfigpool", _MCP_MASTER)
    if not mcp:
        return False
    st = mcp.get("status") or {}
    if st.get("degradedMachineCount") not in (None, 0):
        return False
    for cond in st.get("conditions", []):
        if cond.get("type") == "Updated" and cond.get("status") == "True":
            return True
    total = st.get("machineCount")
    updated = st.get("updatedMachineCount")
    return total is not None and updated == total


def _master_node_names(kubeconfig) -> list:
    data = _oc_json(
        kubeconfig,
        "get",
        "nodes",
        "-l",
        "node-role.kubernetes.io/master=",
    )
    if not data:
        return []
    names = []
    for item in data.get("items", []):
        name = item.get("metadata", {}).get("name")
        if name and name not in names:
            names.append(name)
    return names


def _parse_node_names_from_messages(text: str) -> list:
    found = _NODE_REPORTING_RE.findall(text)
    out = []
    for name in found:
        if name not in out:
            out.append(name)
    return out


def _machineconfig_exists(kubeconfig, name: str) -> bool:
    if not name:
        return False
    result = _oc_run(kubeconfig, "get", "machineconfig", name)
    return result.returncode == 0


def _pool_rendered_config(kubeconfig) -> str | None:
    mcp = _oc_json(kubeconfig, "get", "machineconfigpool", _MCP_MASTER)
    if not mcp:
        return None
    name = (mcp.get("spec") or {}).get("configuration", {}).get("name")
    return name if name else None


def _align_node_mc_annotations(kubeconfig, node: str, rendered: str) -> bool:
    node_obj = _oc_json(kubeconfig, "get", "node", node)
    if not node_obj:
        return False
    ann = (node_obj.get("metadata") or {}).get("annotations") or {}
    cur = ann.get("machineconfiguration.openshift.io/currentConfig", "")
    des = ann.get("machineconfiguration.openshift.io/desiredConfig", "")
    need = cur != rendered or des != rendered
    if cur and not _machineconfig_exists(kubeconfig, cur):
        print(f"  Node {node} references missing MachineConfig: currentConfig={cur}")
        need = True
    if not need:
        return False
    print(f"  Aligning node {node} MachineConfig annotations to {rendered}")
    result = _oc_run(
        kubeconfig,
        "annotate",
        "node",
        node,
        f"machineconfiguration.openshift.io/currentConfig={rendered}",
        f"machineconfiguration.openshift.io/desiredConfig={rendered}",
        "--overwrite",
    )
    return result.returncode == 0


def _workload_pinning_bytes(kubeconfig, rendered: str) -> bytes | None:
    mc = _oc_json(kubeconfig, "get", "machineconfig", rendered)
    if not mc:
        return None
    for fspec in (mc.get("spec") or {}).get("config", {}).get("storage", {}).get("files", []):
        if fspec.get("path") != _WLP_PATH:
            continue
        source = (fspec.get("contents") or {}).get("source", "")
        prefix = "data:text/plain;charset=utf-8;base64,"
        if not source.startswith(prefix):
            return None
        try:
            return base64.b64decode(source[len(prefix):])
        except (ValueError, TypeError):
            return None
    return None


def _sync_workload_pinning(kubeconfig, node: str, rendered: str) -> bool:
    content = _workload_pinning_bytes(kubeconfig, rendered)
    if content is None:
        return False
    b64 = base64.b64encode(content).decode("ascii")
    script = (
        f"echo {b64} | base64 -d > {_WLP_PATH} && "
        f"chmod 644 {_WLP_PATH}"
    )
    print(f"  Syncing {_WLP_PATH} on {node} from {rendered}")
    try:
        result = _oc_run(
            kubeconfig,
            "debug",
            f"node/{node}",
            "--",
            "chroot",
            "/host",
            "sh",
            "-c",
            script,
            timeout=120,
        )
    except subprocess.TimeoutExpired:
        print(
            f"  WARNING: oc debug timed out syncing {_WLP_PATH} on {node}; "
            "continuing with annotation/CSR/MCD remediation.",
            flush=True,
        )
        return False
    if result.returncode != 0:
        err = (result.stderr or result.stdout or "").strip()
        print(f"  WARNING: workload-pinning sync failed on {node}: {err}", flush=True)
        return False
    return True


def _should_sync_workload_pinning(node_obj: dict, fix_wlp: bool) -> bool:
    if fix_wlp:
        return True
    ann = (node_obj.get("metadata") or {}).get("annotations") or {}
    reason = ann.get("machineconfiguration.openshift.io/reason", "")
    markers = ("openshift-workload-pinning", "current config on disk during bootstrap")
    return any(m in reason for m in markers)


def _clear_stale_node_mc_state(kubeconfig, node: str, rendered: str) -> bool:
    node_obj = _oc_json(kubeconfig, "get", "node", node)
    if not node_obj:
        return False
    ann = (node_obj.get("metadata") or {}).get("annotations") or {}
    state = ann.get("machineconfiguration.openshift.io/state", "")
    reason = ann.get("machineconfiguration.openshift.io/reason", "")
    cur = ann.get("machineconfiguration.openshift.io/currentConfig", "")
    if state != "Degraded" and "currentconfig" not in reason.lower():
        return False
    if cur != rendered or not _mcp_master_updated(kubeconfig):
        return False
    print(f"  Clearing stale MachineConfig Degraded state on {node}")
    result = _oc_run(
        kubeconfig,
        "annotate",
        "node",
        node,
        "machineconfiguration.openshift.io/reason-",
        "machineconfiguration.openshift.io/state=Done",
        "--overwrite",
    )
    return result.returncode == 0


def _pending_csr_names(kubeconfig) -> list:
    data = _oc_json(kubeconfig, "get", "csr")
    if not data:
        return []
    pending = []
    for item in data.get("items", []):
        meta = item.get("metadata") or {}
        name = meta.get("name")
        if not name:
            continue
        approved = any(
            c.get("type") == "Approved" and c.get("status") == "True"
            for c in (item.get("status") or {}).get("conditions", [])
        )
        if not approved:
            pending.append(name)
    return pending


def _approve_pending_csrs(kubeconfig) -> int:
    pending = _pending_csr_names(kubeconfig)
    if not pending:
        return 0
    print(f"  Approving {len(pending)} pending CSR(s) ...")
    result = _oc_run(
        kubeconfig,
        "adm",
        "certificate",
        "approve",
        *pending,
    )
    if result.returncode != 0:
        err = (result.stderr or result.stdout or "").strip()
        print(f"  WARNING: CSR approval failed: {err}", flush=True)
        return 0
    return len(pending)


def _restart_mcd_pods(kubeconfig, nodes: list) -> int:
    restarted = 0
    for node in nodes:
        pods = _oc_json(
            kubeconfig,
            "get",
            "pods",
            "-n",
            _MCO_NS,
            "-l",
            _MCD_LABEL,
            "--field-selector",
            f"spec.nodeName={node}",
        )
        if not pods:
            continue
        for pod in pods.get("items", []):
            meta = pod.get("metadata") or {}
            name = meta.get("name")
            if not name:
                continue
            phase = (pod.get("status") or {}).get("phase", "")
            extra = ["--grace-period=0", "--force"] if phase == "Terminating" else []
            print(f"  Restarting MCD pod {name} on {node}")
            result = _oc_run(
                kubeconfig,
                "delete",
                "pod",
                name,
                "-n",
                _MCO_NS,
                *extra,
                "--wait=false",
            )
            if result.returncode == 0:
                restarted += 1
    return restarted


def _wait_machine_config_recovery(kubeconfig, timeout_sec: int) -> None:
    if timeout_sec <= 0:
        return
    print(f"  Waiting up to {timeout_sec}s for machine-config to recover ...", flush=True)
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        if not _co_machine_config_degraded(kubeconfig) and not _mcp_master_needs_work(kubeconfig):
            print("  machine-config ClusterOperator and master pool look healthy.", flush=True)
            return
        time.sleep(15)
    print("  WARNING: machine-config still not fully healthy after remediation wait.", flush=True)


def maybe_remediate_machine_config(kubeconfig_path, args=None) -> bool:
    """Remediate stuck machine-config during install waits (best-effort).

    Aligns stale node annotations, approves pending CSRs, syncs workload-pinning,
    clears stale Degraded node state, and restarts machine-config-daemon pods.
    Returns True if any remediation action ran.
    """
    if _skip_mc_remediation(args):
        return False

    kubeconfig = str(Path(kubeconfig_path).resolve())
    if not Path(kubeconfig).is_file():
        return False
    if not shutil.which("oc"):
        print("  WARNING: oc not in PATH; skipping machine-config remediation.", flush=True)
        return False

    blob = _machine_config_diagnostic_text(kubeconfig)
    if not _matches_mcd_issue(blob):
        return False
    if not (_co_machine_config_degraded(kubeconfig) or _mcp_master_needs_work(kubeconfig)):
        return False

    rendered = _pool_rendered_config(kubeconfig)
    if not rendered:
        print("  WARNING: could not read machineconfigpool/master rendered config.", flush=True)
        return False
    if not _machineconfig_exists(kubeconfig, rendered):
        print(
            f"  WARNING: rendered MachineConfig {rendered} not found; "
            "skipping node annotation remediation.",
            flush=True,
        )
        rendered = None

    nodes = _parse_node_names_from_messages(blob) or _master_node_names(kubeconfig)
    if not nodes:
        print("  WARNING: could not determine master node name for MCO remediation.", flush=True)
        return False

    print(SEPARATOR)
    print("[MCO remediation] Stuck machine-config detected; applying recovery steps ...")
    print(SEPARATOR)

    acted = False
    fix_wlp = os.environ.get("FIX_WORKLOAD_PINNING", "1").strip().lower() not in ("0", "false", "no")

    n_csr = _approve_pending_csrs(kubeconfig)
    acted = acted or n_csr > 0

    if rendered:
        for node in nodes:
            if _align_node_mc_annotations(kubeconfig, node, rendered):
                acted = True
            node_obj = _oc_json(kubeconfig, "get", "node", node)
            if node_obj and _should_sync_workload_pinning(node_obj, fix_wlp):
                if _workload_pinning_bytes(kubeconfig, rendered) is not None:
                    if _sync_workload_pinning(kubeconfig, node, rendered):
                        acted = True

    n_mcd = _restart_mcd_pods(kubeconfig, nodes)
    acted = acted or n_mcd > 0

    if rendered:
        for node in nodes:
            if _clear_stale_node_mc_state(kubeconfig, node, rendered):
                acted = True

    if acted:
        _wait_machine_config_recovery(kubeconfig, _mc_remediation_wait_sec(args))
    else:
        print("  (no machine-config remediation actions were applied)", flush=True)

    print(SEPARATOR)
    return acted


def _timing_seconds(env_key: str, default: int) -> int:
    """Parse non-negative sleep duration from env; invalid or empty → default."""
    raw = os.environ.get(env_key, "").strip()
    if not raw:
        return max(0, int(default))
    try:
        return max(0, int(raw))
    except ValueError:
        return max(0, int(default))


def _timing_pause(description: str, env_key: str, default_seconds: int) -> None:
    secs = _timing_seconds(env_key, default_seconds)
    if secs <= 0:
        return
    print(f"Waiting {secs}s ({description}); env={env_key} ...", flush=True)
    time.sleep(secs)


def _agent_iso_http_url(args):
    u = getattr(args, "iso_url", None)
    if u:
        return u
    remote_host = _attr(args, "remote_host")
    return f"http://{remote_host}:8080/OSs/agent.x86_64.iso"


def _probe_agent_iso_http(url: str) -> None:
    """Confirm the BMC will get a sane HTTP response when fetching the mounted ISO."""
    req = urllib.request.Request(url)
    req.add_header("Range", "bytes=0-0")
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            code = getattr(resp, "status", resp.getcode())
            if code not in (200, 206):
                raise InstallerError(f"ISO HTTP probe failed: {url} HTTP {code}")
            resp.read(1)
        print(f"  ISO HTTP probe OK (range GET): {url}")
    except Exception as e:
        raise InstallerError(f"ISO HTTP probe failed for {url}: {e}") from e


class InstallerError(Exception):
    """Raised when an installation step fails."""


# ---------------------------------------------------------------------------
# Lazy sushy import — non-iDRAC commands work without sushy installed
# ---------------------------------------------------------------------------

_sushy_module = None


def _get_sushy():
    global _sushy_module
    if _sushy_module is None:
        import urllib3
        urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
        import sushy
        _sushy_module = sushy
    return _sushy_module


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _attr(args, name):
    val = getattr(args, name, None)
    if val is None:
        return DEFAULTS.get(name)
    return val


def run_cmd(cmd, check=True, capture=False):
    if isinstance(cmd, str):
        cmd = cmd.split()
    print(f"  > {' '.join(cmd)}")
    kwargs = {}
    if capture:
        kwargs.update(capture_output=True, text=True)
    result = subprocess.run(cmd, check=check, **kwargs)
    return result


def decrypt_password(enc_file="idrac_pw.enc", passphrase=None):
    enc_path = Path(enc_file)
    if not enc_path.exists():
        raise InstallerError(f"{enc_file} not found")
    if passphrase is None:
        passphrase = getpass.getpass("Enter passphrase to decrypt iDRAC password: ")
    # Try pbkdf2 first (modern openssl 3.x), fall back to legacy derivation
    for extra_flags in (["-pbkdf2"], []):
        result = subprocess.run(
            ["openssl", "enc", "-aes-256-cbc", "-d", *extra_flags,
             "-in", str(enc_path), "-pass", f"pass:{passphrase}"],
            capture_output=True, text=True,
        )
        if result.returncode == 0:
            break
    if result.returncode != 0:
        raise InstallerError("Decrypt failed (wrong passphrase or corrupt file)")
    return result.stdout.strip()


def resolve_password(args):
    pw = getattr(args, "password", None) or os.environ.get("IDRAC_PW", "")
    if pw:
        return pw
    enc_file = Path("idrac_pw.enc")
    if enc_file.exists():
        return decrypt_password(str(enc_file))
    raise InstallerError(
        "No iDRAC password: set IDRAC_PW env, use --password, or provide idrac_pw.enc"
    )


# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------

def check_command(name):
    return shutil.which(name) is not None


def ensure_sushy():
    try:
        __import__("sushy")
        __import__("sushy_oem_idrac")
        print("  sushy + sushy-oem-idrac: OK")
        return True
    except ImportError:
        pass
    print("  Installing sushy and sushy-oem-idrac ...")
    result = subprocess.run(
        [sys.executable, "-m", "pip", "install", "--quiet", "sushy", "sushy-oem-idrac"],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        print(f"  ERROR: pip install failed: {result.stderr}", file=sys.stderr)
        return False
    try:
        __import__("sushy")
        __import__("sushy_oem_idrac")
        print("  sushy + sushy-oem-idrac: installed OK")
        return True
    except ImportError:
        print("  ERROR: sushy still not importable after install.", file=sys.stderr)
        return False


def ensure_nmstatectl():
    if check_command("nmstatectl"):
        print("  nmstatectl: OK")
        return True
    os_id, os_like = _os_id_like()
    install_cmd = None
    rpm_families = ("fedora", "rhel", "centos", "rocky", "alma", "ol")
    deb_families = ("debian", "ubuntu")
    if os_id in rpm_families or any(f in os_like for f in rpm_families):
        install_cmd = ["dnf", "install", "-y", "nmstate"]
    elif os_id in deb_families or any(f in os_like for f in deb_families):
        install_cmd = ["apt-get", "install", "-y", "nmstate"]
    if install_cmd is None:
        print(f"  ERROR: Cannot auto-install nmstate (ID={os_id}).", file=sys.stderr)
        return False
    if os.getuid() != 0:
        install_cmd = ["sudo"] + install_cmd
    if "apt-get" in install_cmd:
        update_cmd = (["sudo"] if os.getuid() != 0 else []) + ["apt-get", "update", "-qq"]
        subprocess.run(update_cmd, capture_output=True, text=True)
    print(f"  Installing nmstate ...")
    result = subprocess.run(install_cmd, capture_output=True, text=True)
    if result.returncode != 0 and "apt-get" in install_cmd:
        # On Debian/Ubuntu nmstate often not in repo; try pip (e.g. into .venv)
        print("  apt nmstate failed, trying pip install nmstate ...")
        pip_result = subprocess.run(
            [sys.executable, "-m", "pip", "install", "nmstate"],
            capture_output=True, text=True,
        )
        if pip_result.returncode == 0:
            bindir = str(Path(sys.executable).resolve().parent)
            os.environ["PATH"] = bindir + os.pathsep + os.environ.get("PATH", "")
            if check_command("nmstatectl"):
                print("  nmstatectl: installed OK (via pip)")
                return True
        print("  ERROR: nmstate install failed (apt and pip).", file=sys.stderr)
        return False
    if result.returncode != 0:
        print(f"  ERROR: nmstate install failed.", file=sys.stderr)
        return False
    if check_command("nmstatectl"):
        print("  nmstatectl: installed OK")
        return True
    print("  ERROR: nmstatectl still not in PATH.", file=sys.stderr)
    return False


def _os_id_like():
    """Return (os_id, os_like) from /etc/os-release."""
    os_id, os_like = "", ""
    os_release = Path("/etc/os-release")
    if os_release.exists():
        for line in os_release.read_text().splitlines():
            if line.startswith("ID="):
                os_id = line.split("=", 1)[1].strip('"')
            elif line.startswith("ID_LIKE="):
                os_like = line.split("=", 1)[1].strip('"')
    return os_id, os_like


def ensure_sshpass():
    """Install sshpass if missing (Debian/Ubuntu/RHEL/Fedora)."""
    if check_command("sshpass"):
        print("  sshpass: OK")
        return True
    os_id, os_like = _os_id_like()
    install_cmd = None
    rpm_families = ("fedora", "rhel", "centos", "rocky", "alma", "ol")
    deb_families = ("debian", "ubuntu")
    if os_id in rpm_families or any(f in os_like for f in rpm_families):
        install_cmd = ["dnf", "install", "-y", "sshpass"]
    elif os_id in deb_families or any(f in os_like for f in deb_families):
        install_cmd = ["apt-get", "install", "-y", "sshpass"]
    if install_cmd is None:
        print("  ERROR: Cannot auto-install sshpass (install manually or use supported OS).", file=sys.stderr)
        return False
    if os.getuid() != 0:
        install_cmd = ["sudo"] + install_cmd
    if "apt-get" in install_cmd:
        update_cmd = (["sudo"] if os.getuid() != 0 else []) + ["apt-get", "update", "-qq"]
        subprocess.run(update_cmd, capture_output=True, text=True)
    print("  Installing sshpass ...")
    result = subprocess.run(install_cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print("  ERROR: sshpass install failed.", file=sys.stderr)
        return False
    if check_command("sshpass"):
        print("  sshpass: installed OK")
        return True
    print("  ERROR: sshpass still not in PATH.", file=sys.stderr)
    return False


def cmd_preflight(args):
    print("Checking prerequisites ...")
    ok = True
    if not ensure_sushy():
        ok = False
    if not ensure_nmstatectl():
        ok = False
    if not ensure_sshpass():
        ok = False
    for tool in ("oc", "openssl"):
        if check_command(tool):
            print(f"  {tool}: OK")
        else:
            print(f"  {tool}: NOT FOUND", file=sys.stderr)
            ok = False
    if ok:
        print("All prerequisites satisfied.")
    else:
        raise InstallerError("Some prerequisites are missing")


# ---------------------------------------------------------------------------
# SSH key
# ---------------------------------------------------------------------------

def cmd_ensure_ssh_key(args):
    ssh_pub = Path(_attr(args, "ssh_key"))
    ssh_priv = ssh_pub.parent / ssh_pub.stem

    if not ssh_pub.exists():
        print(f"Generating SSH key at {ssh_pub} ...")
        run_cmd(["ssh-keygen", "-t", "ed25519", "-f", str(ssh_priv), "-N", "", "-q"])
        print("  SSH key generated.")
    else:
        print(f"SSH key exists: {ssh_pub}")

    remote_user = _attr(args, "remote_user")
    remote_host = _attr(args, "remote_host")
    pw = resolve_password(args)
    print(f"Copying SSH key to {remote_user}@{remote_host} ...")
    run_cmd([
        "sshpass", "-p", pw, "ssh-copy-id", "-i", str(ssh_pub),
        "-o", "StrictHostKeyChecking=no", f"{remote_user}@{remote_host}",
    ])
    print("  SSH key copied.")

    # Reinstalls rotate the SNO host key; drop stale known_hosts entries so
    # post-failure diagnostics (SSH from webcache → node) keep working.
    cluster_ip = _attr(args, "cluster_ip") or DEFAULTS.get("cluster_ip")
    if cluster_ip:
        _forget_ssh_host_key(cluster_ip)
        _forget_remote_ssh_host_key(remote_user, remote_host, cluster_ip)


def _forget_ssh_host_key(host: str) -> None:
    known = Path.home() / ".ssh" / "known_hosts"
    if not known.is_file():
        return
    print(f"Removing stale local known_hosts entry for {host} ...")
    subprocess.run(
        ["ssh-keygen", "-f", str(known), "-R", host],
        check=False,
        capture_output=True,
        text=True,
    )


def _forget_remote_ssh_host_key(remote_user: str, remote_host: str, host: str) -> None:
    print(f"Removing stale known_hosts entry for {host} on {remote_user}@{remote_host} ...")
    result = subprocess.run(
        [
            "ssh",
            "-o", "StrictHostKeyChecking=no",
            "-o", "BatchMode=yes",
            f"{remote_user}@{remote_host}",
            f"ssh-keygen -R {host} >/dev/null 2>&1 || true",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print(
            f"  WARNING: could not clear remote known_hosts for {host} "
            f"(exit {result.returncode}); diagnostics SSH may warn on host-key change.",
            flush=True,
        )
    else:
        print(f"  Cleared {host} from {remote_host} known_hosts.")


# ---------------------------------------------------------------------------
# Extract openshift-install
# ---------------------------------------------------------------------------

def cmd_extract_installer(args):
    ocp_version = _attr(args, "ocp_version")
    registry_auth = _attr(args, "registry_auth")

    if not Path(registry_auth).exists():
        raise InstallerError(f"Registry auth not found: {registry_auth}")

    release_image = f"quay.io/openshift-release-dev/ocp-release:{ocp_version}-x86_64"
    print(f"Getting release digest for {release_image} ...")

    result = run_cmd(
        ["oc", "adm", "release", "info", release_image,
         "--registry-config", registry_auth],
        capture=True,
    )

    digest = None
    for line in result.stdout.splitlines():
        if "Pull From:" in line:
            digest = line.split()[-1]
            break
    if not digest:
        raise InstallerError("Could not parse release digest from oc output")

    print(f"  RELEASE_DIGEST={digest}")
    print(SEPARATOR)
    print("Extracting openshift-install ...")
    run_cmd([
        "oc", "adm", "release", "extract", "-a", registry_auth,
        "--command=openshift-install", digest,
    ])
    print("  openshift-install extracted.")


# ---------------------------------------------------------------------------
# Config preparation
# ---------------------------------------------------------------------------

def template_install_config(src, dst, registry_auth_path, ssh_key_path):
    with open(registry_auth_path) as f:
        pull_secret = json.dumps(json.load(f))
    pull_secret_escaped = pull_secret.replace("'", "''")
    ssh_key = Path(ssh_key_path).read_text().strip()

    content = Path(src).read_text()
    content = content.replace('{"auths":{<pull_secret>}}', pull_secret_escaped)
    content = content.replace("ssh-ed25519 <ssh_key> <user>@<host>", ssh_key)
    Path(dst).write_text(content)


def cmd_prepare_configs(args):
    workdir = Path(_attr(args, "workdir"))
    src_dir = Path(_attr(args, "src_dir"))
    registry_auth = _attr(args, "registry_auth")
    ssh_key = _attr(args, "ssh_key")

    for required in [
        src_dir / "openshift",
        src_dir / "agent-config.yaml",
        src_dir / "install-config.yaml",
    ]:
        if not required.exists():
            raise InstallerError(f"Required source not found: {required}")
    if not Path(registry_auth).exists():
        raise InstallerError(f"Registry auth not found: {registry_auth}")

    if workdir.exists():
        print(f"Cleaning {workdir} ...")
        shutil.rmtree(workdir)
    workdir.mkdir(parents=True, exist_ok=True)

    print(f"Copying {src_dir}/openshift -> {workdir}/openshift ...")
    shutil.copytree(src_dir / "openshift", workdir / "openshift")

    print("Copying agent-config.yaml ...")
    shutil.copy2(src_dir / "agent-config.yaml", workdir / "agent-config.yaml")

    print("Templating install-config.yaml ...")
    template_install_config(
        src_dir / "install-config.yaml",
        workdir / "install-config.yaml",
        registry_auth,
        ssh_key,
    )
    print(f"  Templated {workdir}/install-config.yaml with pullSecret and sshKey.")


# ---------------------------------------------------------------------------
# Build ISO
# ---------------------------------------------------------------------------

def cmd_build_iso(args):
    workdir = _attr(args, "workdir")
    installer = _attr(args, "installer")

    if not Path(installer).exists():
        raise InstallerError(f"Installer not found: {installer}")

    print("Building agent ISO ...")
    print(SEPARATOR)
    run_cmd([installer, "agent", "create", "image", "--dir", workdir, "--log-level", "debug"])
    print(SEPARATOR)

    iso_path = Path(workdir) / "agent.x86_64.iso"
    if iso_path.exists():
        size_mb = iso_path.stat().st_size / (1024 * 1024)
        print(f"  ISO created: {iso_path} ({size_mb:.1f} MB)")
    else:
        raise InstallerError(f"ISO not found after build: {iso_path}")


# ---------------------------------------------------------------------------
# Copy ISO to webcache
# ---------------------------------------------------------------------------

def cmd_copy_iso(args):
    workdir = Path(_attr(args, "workdir"))
    remote_user = _attr(args, "remote_user")
    remote_host = _attr(args, "remote_host")
    remote_path = _attr(args, "remote_path")

    iso_path = workdir / "agent.x86_64.iso"
    if not iso_path.exists():
        raise InstallerError(f"ISO not found: {iso_path}")

    dest = f"{remote_user}@{remote_host}:{remote_path}"
    print(f"Copying {iso_path} -> {dest} ...")
    run_cmd(["scp", str(iso_path), dest])
    print("  ISO copied.")

    _timing_pause("post-SCP filesystem / HTTP export settle", "POST_COPY_ISO_SLEEP_SEC", 0)

    probe = os.environ.get("ISO_HTTP_PROBE", "").strip().lower()
    if probe in ("1", "true", "yes", "on"):
        url = _agent_iso_http_url(args)
        print(f"Probing agent ISO HTTP reachability ({url}) ...")
        _probe_agent_iso_http(url)


# ---------------------------------------------------------------------------
# iDRAC operations (sushy)
# ---------------------------------------------------------------------------

def connect(ip, user, password):
    sushy = _get_sushy()
    root = sushy.Sushy(f"https://{ip}", username=user, password=password, verify=False)
    managers = root.get_manager_collection().get_members()
    if not managers:
        raise InstallerError("No Redfish managers found on BMC")
    manager = managers[0]
    systems = root.get_system_collection().get_members()
    if not systems:
        raise InstallerError("No Redfish systems found on BMC")
    system = systems[0]
    return root, manager, system


def find_cd_device(manager):
    sushy = _get_sushy()
    for vm in manager.virtual_media.get_members():
        if vm.media_types and sushy.VIRTUAL_MEDIA_CD in vm.media_types:
            return vm
    return None


def require_cd(manager):
    cd = find_cd_device(manager)
    if cd is None:
        raise InstallerError("No VirtualCD device found on iDRAC")
    return cd


def insert_virtual_media(cd, iso_url):
    """Mount ISO from HTTP URL on VirtualCD; raise InstallerError with Redfish details on failure."""
    sushy = _get_sushy()
    try:
        cd.insert_media(iso_url)
    except sushy.exceptions.ServerSideError as e:
        raise InstallerError(
            "Virtual media insert failed: iDRAC rejected the mount or could not fetch the ISO.\n"
            f"  URL: {iso_url}\n"
            f"  Redfish: {e}\n"
            "  Check: iDRAC management network can reach that host:port (routing/firewall), "
            "HTTP serves the file (curl from a host on the same net as the BMC), "
            "and the path matches where copy-iso placed agent.x86_64.iso."
        ) from e
    except Exception as e:
        raise InstallerError(f"Virtual media insert failed: {e}") from e


def cmd_status(args):
    pw = resolve_password(args)
    _, manager, system = connect(args.ip, args.user, pw)
    print(SEPARATOR)
    print(f"  Model:       {system.model or 'N/A'}")
    print(f"  Power state: {system.power_state}")
    cd = find_cd_device(manager)
    if cd:
        print(f"  VMedia CD:   inserted={cd.inserted}  image={cd.image}")
    else:
        print("  VMedia CD:   (no VirtualCD device found)")
    print(SEPARATOR)


def cmd_eject(args):
    sushy = _get_sushy()
    pw = resolve_password(args)
    _, manager, _ = connect(args.ip, args.user, pw)
    cd = require_cd(manager)
    try:
        cd.eject_media()
        print("Virtual media ejected.")
    except sushy.exceptions.ServerSideError:
        print("No media was mounted (nothing to eject).")
    except Exception as e:
        print(f"Eject skipped: {e}")


def cmd_insert(args):
    pw = resolve_password(args)
    _, manager, _ = connect(args.ip, args.user, pw)
    cd = require_cd(manager)
    print(f"Inserting virtual media: {args.iso_url}")
    insert_virtual_media(cd, args.iso_url)
    time.sleep(5)
    cd.invalidate()
    cd.refresh(force=False)
    print(f"  Inserted: {cd.inserted}  Image: {cd.image}")


def cmd_set_boot_cd(args):
    sushy = _get_sushy()
    pw = resolve_password(args)
    _, manager, _ = connect(args.ip, args.user, pw)
    oem = manager.get_oem_extension("Dell")
    oem.set_virtual_boot_device(sushy.VIRTUAL_MEDIA_CD, persistent=False, manager=manager)
    print("Boot device set to VirtualCD (one-time).")


def cmd_set_boot_hdd(args):
    sushy = _get_sushy()
    pw = resolve_password(args)
    _, manager, _ = connect(args.ip, args.user, pw)
    oem = manager.get_oem_extension("Dell")
    oem.set_virtual_boot_device(sushy.VIRTUAL_MEDIA_HDD, persistent=False, manager=manager)
    print("Boot device set to HDD (one-time).")


def cmd_restart(args):
    sushy = _get_sushy()
    pw = resolve_password(args)
    _, _, system = connect(args.ip, args.user, pw)
    system.reset_system(sushy.RESET_TYPE_FORCE_RESTART)
    print("Force restart command sent.")


def cmd_power_on(args):
    sushy = _get_sushy()
    pw = resolve_password(args)
    _, _, system = connect(args.ip, args.user, pw)
    system.reset_system(sushy.RESET_TYPE_ON)
    print("Power on command sent.")


def cmd_power_off(args):
    sushy = _get_sushy()
    pw = resolve_password(args)
    _, _, system = connect(args.ip, args.user, pw)
    system.reset_system(sushy.RESET_TYPE_FORCE_OFF)
    print("Force power off command sent.")


def cmd_wait_power_on(args):
    sushy = _get_sushy()
    pw = resolve_password(args)
    _, _, system = connect(args.ip, args.user, pw)
    max_attempts = getattr(args, "attempts", 30)
    interval = getattr(args, "interval", 10)
    for attempt in range(1, max_attempts + 1):
        system.invalidate()
        system.refresh(force=False)
        state = system.power_state
        if state == sushy.SYSTEM_POWER_STATE_ON:
            print("Server is powered ON.")
            return
        print(f"  [{attempt}/{max_attempts}] state: {state}")
        time.sleep(interval)
    raise InstallerError("Timeout waiting for server to power on")


def cmd_deploy(args):
    """Full iDRAC deploy cycle: eject -> insert -> set-boot-cd -> restart -> wait."""
    sushy = _get_sushy()
    pw = resolve_password(args)
    iso_url = args.iso_url

    print(SEPARATOR)
    print(f"Connecting to iDRAC at {args.ip} ...")
    root, manager, system = connect(args.ip, args.user, pw)
    print(f"  Model: {system.model or 'N/A'}")
    print(f"  Power: {system.power_state}")
    print(SEPARATOR)

    cd = require_cd(manager)

    # 1 — Eject
    print("Ejecting existing virtual media ...")
    try:
        cd.eject_media()
        print("  Ejected.")
    except sushy.exceptions.ServerSideError:
        print("  Nothing to eject.")
    except Exception as e:
        print(f"  Eject skipped: {e}")
    _timing_pause("after Virtual CD eject", "IDRAC_DEPLOY_AFTER_EJECT_SEC", 15)

    # 2 — Insert
    print(f"Inserting virtual media: {iso_url}")
    insert_virtual_media(cd, iso_url)
    _timing_pause(
        "after Virtual CD insert / HTTP mount (lets BMC pick up ISO)",
        "IDRAC_DEPLOY_AFTER_INSERT_SEC",
        10,
    )
    cd.invalidate()
    cd.refresh(force=False)
    print(f"  Inserted: {cd.inserted}  Image: {cd.image}")
    print(SEPARATOR)

    # 3 — Set one-time boot to VirtualCD
    print("Setting one-time boot to VirtualCD ...")
    oem = manager.get_oem_extension("Dell")
    oem.set_virtual_boot_device(sushy.VIRTUAL_MEDIA_CD, persistent=False, manager=manager)
    print("  Boot device set via Dell OEM Redfish extension.")
    print(SEPARATOR)

    _timing_pause(
        "after boot order set (before ForceRestart)",
        "IDRAC_DEPLOY_BEFORE_RESTART_SEC",
        0,
    )

    # 4 — Force restart
    print("Restarting server (ForceRestart) ...")
    system.reset_system(sushy.RESET_TYPE_FORCE_RESTART)
    print("  Restart command sent.")
    _timing_pause("after ForceRestart before polling power", "IDRAC_DEPLOY_AFTER_RESTART_SEC", 30)

    # 5 — Wait for power-on
    print("Waiting for server to power ON ...")
    for attempt in range(1, 31):
        system.invalidate()
        system.refresh(force=False)
        state = system.power_state
        if state == sushy.SYSTEM_POWER_STATE_ON:
            print("  Server is powered ON.")
            print(SEPARATOR)
            print("iDRAC operations complete. Server is booting from VirtualCD.")
            return
        print(f"  [{attempt}/30] state: {state}")
        time.sleep(10)

    raise InstallerError("Timeout waiting for server to power on")


# ---------------------------------------------------------------------------
# Wait for install-complete
# ---------------------------------------------------------------------------

def cmd_wait_install(args):
    workdir = _attr(args, "workdir")
    installer = _attr(args, "installer")
    attempts = max(1, int(getattr(args, "install_wait_attempts", _default_install_wait_attempts())))

    if not Path(installer).exists():
        raise InstallerError(f"Installer not found: {installer}")

    kubeconfig = Path(workdir) / "auth" / "kubeconfig"
    os.environ["KUBECONFIG"] = str(kubeconfig.resolve())

    cmd = [installer, "agent", "wait-for", "install-complete", "--dir", workdir]
    for attempt in range(1, attempts + 1):
        # On retries, wait until API is stably up so we do not restart the
        # installer's ~40m cluster-init window while the SNO node is rebooting.
        if attempt > 1 and kubeconfig.is_file():
            wait_for_api_ready(
                kubeconfig,
                workdir=workdir,
                args=args,
                label="API (pre-retry)",
            )
        print(f"Waiting for install-complete (attempt {attempt}/{attempts}) ...")
        try:
            run_cmd(cmd)
            print("Installation complete!")
            return
        except CalledProcessError as e:
            if attempt >= attempts:
                raise
            outage = _install_log_suggests_api_outage(workdir)
            if outage:
                print(
                    "Install wait failed while installer logs show API connectivity "
                    "loss (no route to host / connection refused / similar). "
                    "This is common during SNO MachineConfig reboot; waiting for "
                    "API recovery before retry ...",
                    flush=True,
                )
            if kubeconfig.is_file():
                wait_for_api_ready(
                    kubeconfig,
                    workdir=workdir,
                    args=args,
                    label="API (post-failure)",
                )
                maybe_remediate_machine_config(kubeconfig, args)
            print(
                f"Install wait exited {e.returncode} (openshift-install allows ~90m per attempt). "
                "Cluster may still be reconciling MachineConfig; retrying ...",
                flush=True,
            )


def cmd_remediate_mco(args):
    """Run machine-config remediation against workdir/auth/kubeconfig."""
    kubeconfig = Path(_attr(args, "workdir")).resolve() / "auth" / "kubeconfig"
    if not kubeconfig.is_file():
        raise InstallerError(f"Kubeconfig not found: {kubeconfig}")
    os.environ["KUBECONFIG"] = str(kubeconfig)
    wait_for_api_ready(kubeconfig, workdir=_attr(args, "workdir"), args=args, label="API")
    if not maybe_remediate_machine_config(kubeconfig, args):
        print("No machine-config remediation was needed or possible.")


def cmd_wait_install_maybe_remediate(args):
    """Run install-complete waits; if they fail but kubeconfig exists, remediate MCO/CSRs
    and run extra wait rounds.
    """
    workdir = _attr(args, "workdir")
    kubeconfig = Path(workdir).resolve() / "auth" / "kubeconfig"
    try:
        cmd_wait_install(args)
    except CalledProcessError:
        remediation = _remediation_install_attempts(args)
        if remediation < 1 or not kubeconfig.is_file():
            raise
        print(SEPARATOR)
        print(
            "[Remediation] Primary install-complete waits failed; "
            "waiting for API readiness before MCO remediation / extra waits."
        )
        wait_for_api_ready(
            kubeconfig,
            workdir=workdir,
            args=args,
            label="API (remediation)",
        )
        maybe_remediate_machine_config(kubeconfig, args)
        print(SEPARATOR)
        print(
            "[Remediation] install-complete waits failed while a kubeconfig exists; "
            "machine-config remediation was attempted; running extra wait-for rounds."
        )
        print(
            f"[Remediation] Running {remediation} extra wait-for install-complete attempt(s); "
            f"Kubeconfig = {kubeconfig}",
            flush=True,
        )
        print(SEPARATOR)
        args.install_wait_attempts = remediation
        cmd_wait_install(args)


# ---------------------------------------------------------------------------
# Full end-to-end install
# ---------------------------------------------------------------------------

def cmd_install(args):
    iso_url = getattr(args, "iso_url", None)
    if not iso_url:
        remote_host = _attr(args, "remote_host")
        iso_url = f"http://{remote_host}:8080/OSs/agent.x86_64.iso"
    args.iso_url = iso_url

    steps = [
        ("Preflight checks", cmd_preflight),
        ("SSH key setup", cmd_ensure_ssh_key),
        ("Extract openshift-install", cmd_extract_installer),
        ("Prepare configurations", cmd_prepare_configs),
        ("Build agent ISO", cmd_build_iso),
        ("Copy ISO to webcache", cmd_copy_iso),
        ("iDRAC deploy (eject -> insert -> boot -> restart -> wait)", cmd_deploy),
        ("Wait for install-complete", cmd_wait_install_maybe_remediate),
    ]
    total = len(steps)

    print(SEPARATOR)
    print("SNO OpenShift Installation")
    print(SEPARATOR)

    for i, (label, func) in enumerate(steps, 1):
        print(f"\n[{i}/{total}] {label}")
        print(SEPARATOR)
        func(args)
        print(SEPARATOR)

    print("\nInstallation finished successfully!")
    print(SEPARATOR)


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def build_parser():
    parser = argparse.ArgumentParser(
        description="SNO OpenShift installer with iDRAC management via sushy (Redfish)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    # Common options
    parser.add_argument("--ip", default=os.environ.get("IDRAC_IP", DEFAULTS["idrac_ip"]),
                        help="iDRAC IP address (env: IDRAC_IP)")
    parser.add_argument("--user", default=os.environ.get("IDRAC_USER", DEFAULTS["idrac_user"]),
                        help="iDRAC username (env: IDRAC_USER)")
    parser.add_argument("--password", default=os.environ.get("IDRAC_PW"),
                        help="iDRAC password (env: IDRAC_PW)")
    parser.add_argument("--workdir", default=DEFAULTS["workdir"],
                        help="Working directory for build artifacts")
    parser.add_argument("--src-dir", dest="src_dir", default=DEFAULTS["src_dir"],
                        help="Source config directory (install-config, agent-config)")
    parser.add_argument("--ocp-version", dest="ocp_version", default=DEFAULTS["ocp_version"],
                        help="OpenShift version to install")
    parser.add_argument("--installer", default=DEFAULTS["installer"],
                        help="Path to openshift-install binary")
    parser.add_argument("--remote-user", dest="remote_user", default=DEFAULTS["remote_user"],
                        help="Webcache host SSH user")
    parser.add_argument("--remote-host", dest="remote_host", default=DEFAULTS["remote_host"],
                        help="Webcache host IP/hostname")
    parser.add_argument("--remote-path", dest="remote_path", default=DEFAULTS["remote_path"],
                        help="Webcache host ISO destination path")
    parser.add_argument("--cluster-ip", dest="cluster_ip", default=DEFAULTS["cluster_ip"],
                        help="SNO node / rendezvous IP for API readiness probes "
                        f"(default: {DEFAULTS['cluster_ip']})")
    parser.add_argument("--ssh-key", dest="ssh_key", default=DEFAULTS["ssh_key"],
                        help="Path to SSH public key")
    parser.add_argument("--registry-auth", dest="registry_auth", default=DEFAULTS["registry_auth"],
                        help="Path to Docker/registry auth config.json")

    sub = parser.add_subparsers(dest="command")
    sub.required = True

    sub.add_parser("preflight", help="Check/install all prerequisites")
    sub.add_parser("ensure-ssh-key", help="Generate SSH key and copy to webcache host")
    sub.add_parser("extract-installer", help="Extract openshift-install from OCP release")
    sub.add_parser("prepare-configs", help="Prepare workdir with templated configs")
    sub.add_parser("build-iso", help="Build agent ISO")
    sub.add_parser("copy-iso", help="Copy ISO to webcache host via SCP")

    sub.add_parser("status", help="Show iDRAC system status and virtual media")
    sub.add_parser("eject", help="Eject virtual media from VirtualCD slot")

    p_ins = sub.add_parser("insert", help="Insert ISO into VirtualCD slot")
    p_ins.add_argument("iso_url", help="HTTP URL to the ISO file")

    sub.add_parser("set-boot-cd", help="Set one-time boot to VirtualCD (Dell OEM)")
    sub.add_parser("set-boot-hdd", help="Set one-time boot to HDD (Dell OEM)")
    sub.add_parser("restart", help="Force-restart the server")
    sub.add_parser("power-on", help="Power on the server")
    sub.add_parser("power-off", help="Force power off the server")

    p_wait = sub.add_parser("wait-power-on", help="Wait for server to reach powered-on state")
    p_wait.add_argument("--attempts", type=int, default=30, help="Max poll attempts")
    p_wait.add_argument("--interval", type=int, default=10, help="Seconds between polls")

    p_dep = sub.add_parser("deploy", help="iDRAC full cycle: eject -> insert -> boot-cd -> restart -> wait")
    p_dep.add_argument("iso_url", help="HTTP URL to the ISO file")

    p_wi = sub.add_parser("wait-install", help="Wait for openshift-install agent install-complete")
    p_wi.add_argument(
        "--install-wait-attempts",
        dest="install_wait_attempts",
        type=int,
        default=_default_install_wait_attempts(),
        metavar="N",
        help="Retries for openshift-install wait-for install-complete (~90m each). "
        "Default: env INSTALL_WAIT_ATTEMPTS or 2.",
    )
    _add_mc_remediation_args(p_wi)
    _add_api_ready_args(p_wi)

    p_mco = sub.add_parser(
        "remediate-mco",
        help="Remediate stuck machine-config (annotations, CSRs, MCD restart)",
    )
    _add_mc_remediation_args(p_mco)
    _add_api_ready_args(p_mco)

    p_full = sub.add_parser("install", help="Full end-to-end SNO OpenShift installation")
    p_full.add_argument("--iso-url", dest="iso_url", default=None,
                        help="ISO URL for iDRAC (default: http://<remote-host>:8080/OSs/agent.x86_64.iso)")
    p_full.add_argument(
        "--install-wait-attempts",
        dest="install_wait_attempts",
        type=int,
        default=_default_install_wait_attempts(),
        metavar="N",
        help="Retries for openshift-install wait-for install-complete (~90m each). "
        "Default: env INSTALL_WAIT_ATTEMPTS or 2.",
    )
    p_full.add_argument(
        "--remediation-install-wait-attempts",
        dest="remediation_install_wait_attempts",
        type=int,
        default=None,
        metavar="N",
        help="After primary waits fail: if kubeconfig exists, retry install-complete "
        "up to N more times (~90m each). "
        "Default: env REMEDIATION_INSTALL_WAIT_ATTEMPTS or 0 (off).",
    )
    _add_mc_remediation_args(p_full)
    _add_api_ready_args(p_full)

    return parser


def _add_mc_remediation_args(parser):
    parser.add_argument(
        "--skip-mc-remediation",
        dest="skip_mc_remediation",
        action="store_true",
        help="Do not run machine-config remediation (align annotations, approve CSRs, "
        "restart MCD) between install-complete wait attempts. Env: SKIP_MC_REMEDIATION=1.",
    )
    parser.add_argument(
        "--mc-remediation-wait-sec",
        dest="mc_remediation_wait_sec",
        type=int,
        default=None,
        metavar="SEC",
        help="Seconds to wait for machine-config recovery after remediation. "
        "Default: env MC_REMEDIATION_WAIT_SEC or 120.",
    )


def _add_api_ready_args(parser):
    parser.add_argument(
        "--api-ready-wait-sec",
        dest="api_ready_wait_sec",
        type=int,
        default=None,
        metavar="SEC",
        help="Max seconds to wait for kube-apiserver /readyz between install-complete "
        "retries (SNO reboot / no-route-to-host). Default: env API_READY_WAIT_SEC or 1800. "
        "Set 0 to disable.",
    )
    parser.add_argument(
        "--api-ready-poll-sec",
        dest="api_ready_poll_sec",
        type=int,
        default=None,
        metavar="SEC",
        help="Seconds between API readiness probes. "
        "Default: env API_READY_POLL_SEC or 15.",
    )
    parser.add_argument(
        "--api-ready-settle-sec",
        dest="api_ready_settle_sec",
        type=int,
        default=None,
        metavar="SEC",
        help="Seconds to settle after API is stably ready before retrying wait-for. "
        "Default: env API_READY_SETTLE_SEC or 90.",
    )
    parser.add_argument(
        "--api-ready-stable-polls",
        dest="api_ready_stable_polls",
        type=int,
        default=None,
        metavar="N",
        help="Consecutive successful /readyz polls required before settle. "
        "Default: env API_READY_STABLE_POLLS or 3.",
    )


DISPATCH = {
    "preflight": cmd_preflight,
    "ensure-ssh-key": cmd_ensure_ssh_key,
    "extract-installer": cmd_extract_installer,
    "prepare-configs": cmd_prepare_configs,
    "build-iso": cmd_build_iso,
    "copy-iso": cmd_copy_iso,
    "status": cmd_status,
    "eject": cmd_eject,
    "insert": cmd_insert,
    "set-boot-cd": cmd_set_boot_cd,
    "set-boot-hdd": cmd_set_boot_hdd,
    "restart": cmd_restart,
    "power-on": cmd_power_on,
    "power-off": cmd_power_off,
    "wait-power-on": cmd_wait_power_on,
    "deploy": cmd_deploy,
    "wait-install": cmd_wait_install,
    "remediate-mco": cmd_remediate_mco,
    "install": cmd_install,
}


def main():
    parser = build_parser()
    args = parser.parse_args()
    try:
        DISPATCH[args.command](args)
    except InstallerError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("\nAborted.", file=sys.stderr)
        sys.exit(130)


if __name__ == "__main__":
    main()
