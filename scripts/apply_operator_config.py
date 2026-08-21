#!/usr/bin/env python3
"""Apply abi-master-0/extra-manifests/operator-config in a safe order.

1. ConfigMaps (no CRDs).
2. If Machine Config is degraded (e.g. missing ``currentconfig``, master pool
   not ready), restart MCD pods on affected nodes and wait for recovery.
3. Wait for Manual-approval InstallPlans (OLM creates them after Subscriptions),
   then approve all pending InstallPlans. Re-approve during CSV waits so late
   InstallPlans are not left unapproved (race with ``oc apply`` of
   operator-install).
4. Wait for LVMS and SR-IOV ClusterServiceVersions (phase Succeeded).
5. Wait for LVMS CRD, apply LVMCluster.
6. Wait for SR-IOV CRDs, apply Sriov resources.

Uses the Kubernetes Python client (``kubernetes`` package) with kubeconfig
authentication. Set ``KUBECONFIG`` or ``KUBECONFIG_PATH`` to the cluster
kubeconfig file.
"""

from __future__ import annotations

import argparse
import os
import re
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

try:
    from kubernetes import client, config as k8s_config
    from kubernetes.client.exceptions import ApiException
    from kubernetes.utils.create_from_yaml import FailToCreateError, create_from_dict
    import yaml
except ImportError as e:
    raise SystemExit(
        "Missing dependency: install the Kubernetes Python client "
        "(e.g. pip install 'kubernetes>=28.0.0')."
    ) from e


class OperatorConfigError(Exception):
    """Fatal error applying operator-config (exit code 1)."""


# OLM / operators.coreos.com
_OLM_GROUP = "operators.coreos.com"
_OLM_VERSION = "v1alpha1"
_PLURAL_INSTALLPLAN = "installplans"
_PLURAL_CSV = "clusterserviceversions"
_PLURAL_SUBSCRIPTION = "subscriptions"

# Long-running API calls (mirrors prior 600s oc subprocess timeout)
_REQUEST_TIMEOUT_SEC = 600


def _repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def _operator_config_dir() -> Path:
    root = _repo_root()
    return root / "abi-master-0" / "extra-manifests" / "operator-config"


def _resolve_kubeconfig(explicit: str | None) -> Path:
    if explicit:
        p = Path(explicit).expanduser()
        if not p.is_file():
            raise OperatorConfigError(f"Kubeconfig not found: {p}")
        return p.resolve()
    kc = os.environ.get("KUBECONFIG_PATH") or os.environ.get("KUBECONFIG")
    if not kc:
        raise OperatorConfigError(
            "Set KUBECONFIG or KUBECONFIG_PATH to your kubeconfig file path."
        )
    p = Path(kc.strip()).expanduser()
    if not p.is_file():
        raise OperatorConfigError(f"Kubeconfig not found: {p}")
    return p.resolve()


def _load_clients(
    kubeconfig: Path,
) -> tuple[
    client.ApiClient,
    client.CoreV1Api,
    client.CustomObjectsApi,
    client.ApiextensionsV1Api,
]:
    k8s_config.load_kube_config(config_file=str(kubeconfig))
    api_client = client.ApiClient()
    return (
        api_client,
        client.CoreV1Api(api_client),
        client.CustomObjectsApi(api_client),
        client.ApiextensionsV1Api(api_client),
    )


def _rt() -> tuple[int, int]:
    return (_REQUEST_TIMEOUT_SEC, _REQUEST_TIMEOUT_SEC)


def _env_int(name: str, default: int) -> int:
    raw = os.environ.get(name)
    if raw is None or not str(raw).strip():
        return default
    try:
        return int(str(raw).strip())
    except ValueError:
        return default


# Machine Config Operator (MCO) — degraded master pool / missing currentconfig
_CONFIG_GROUP = "config.openshift.io"
_CONFIG_VERSION = "v1"
_PLURAL_CLUSTER_OPERATORS = "clusteroperators"
_MCFG_GROUP = "machineconfiguration.openshift.io"
_MCFG_VERSION = "v1"
_PLURAL_MACHINE_CONFIG_POOLS = "machineconfigpools"
_MCP_MASTER = "master"
_MCO_NS = "openshift-machine-config-operator"
_MCD_LABEL_SELECTOR = "k8s-app=machine-config-daemon"
_MCD_ISSUE_MARKERS = (
    "machine-config-daemon/currentconfig",
    "/etc/machine-config-daemon/currentconfig",
    "machine-config-daemon/currentconfig: no such file",
    "MachineConfigPool master is not ready",
    "syncRequiredMachineConfigPools",
)
_NODE_REPORTING_RE = re.compile(
    r"Node\s+([a-zA-Z0-9.-]+)\s+is\s+reporting",
    re.IGNORECASE,
)
_MC_REMEDY_COOLDOWN_SEC = 300
_MC_REMEDY_MAX_PER_RUN = 10


def _get_cluster_operator(
    custom: client.CustomObjectsApi,
    name: str,
) -> dict[str, Any] | None:
    try:
        obj = custom.get_cluster_custom_object(
            group=_CONFIG_GROUP,
            version=_CONFIG_VERSION,
            plural=_PLURAL_CLUSTER_OPERATORS,
            name=name,
            _request_timeout=_rt(),
        )
    except ApiException as e:
        if e.status in (403, 404):
            return None
        raise OperatorConfigError(
            f"get clusteroperator/{name} failed: {e.reason or e}"
        ) from e
    return obj if isinstance(obj, dict) else None


def _get_machine_config_pool(
    custom: client.CustomObjectsApi,
    name: str,
) -> dict[str, Any] | None:
    try:
        obj = custom.get_cluster_custom_object(
            group=_MCFG_GROUP,
            version=_MCFG_VERSION,
            plural=_PLURAL_MACHINE_CONFIG_POOLS,
            name=name,
            _request_timeout=_rt(),
        )
    except ApiException as e:
        if e.status in (403, 404):
            return None
        raise OperatorConfigError(
            f"get machineconfigpool/{name} failed: {e.reason or e}"
        ) from e
    return obj if isinstance(obj, dict) else None


def _machine_config_diagnostic_blob(
    custom: client.CustomObjectsApi,
) -> str:
    """Concatenate CO + MCP messages for substring matching."""
    parts: list[str] = []
    co = _get_cluster_operator(custom, "machine-config")
    if co:
        for c in co.get("status", {}).get("conditions", []):
            parts.append(str(c.get("message", "")))
            parts.append(str(c.get("reason", "")))
    mcp = _get_machine_config_pool(custom, _MCP_MASTER)
    if mcp:
        for c in mcp.get("status", {}).get("conditions", []):
            parts.append(str(c.get("message", "")))
            parts.append(str(c.get("reason", "")))
    return "\n".join(parts)


def _matches_mcd_currentconfig_issue(diagnostic: str) -> bool:
    t = diagnostic.lower()
    if "currentconfig" in t and "no such file" in t:
        return True
    if "upgrade failure" in t and "currentconfig" in t:
        return True
    if "bootstrap" in t and "currentconfig" in t and "no such file" in t:
        return True
    for m in _MCD_ISSUE_MARKERS:
        if m.lower() in t:
            return True
    return False


def _co_machine_config_degraded(co: dict[str, Any] | None) -> bool:
    if not co:
        return False
    for c in co.get("status", {}).get("conditions", []):
        if c.get("type") == "Degraded" and c.get("status") == "True":
            return True
    return False


def _mcp_master_needs_work(mcp: dict[str, Any] | None) -> bool:
    if not mcp:
        return False
    st = mcp.get("status") or {}
    if st.get("unavailableMachineCount") not in (None, 0):
        return True
    if st.get("degradedMachineCount") not in (None, 0):
        return True
    for c in st.get("conditions", []):
        if c.get("type") == "Degraded" and c.get("status") == "True":
            return True
    return False


def _parse_node_names_from_messages(text: str) -> list[str]:
    found = _NODE_REPORTING_RE.findall(text)
    out: list[str] = []
    for n in found:
        if n not in out:
            out.append(n)
    return out


def _master_node_names(core_v1: client.CoreV1Api) -> list[str]:
    out: list[str] = []
    try:
        nodes = core_v1.list_node(
            label_selector="node-role.kubernetes.io/master=",
            _request_timeout=_rt(),
        )
    except ApiException:
        return out
    for n in nodes.items or []:
        if n.metadata and n.metadata.name:
            out.append(n.metadata.name)
    return out


def _delete_mcd_pods_on_nodes(
    core_v1: client.CoreV1Api,
    node_names: list[str],
) -> int:
    """Delete MCD pods on the given nodes; return how many were deleted."""
    deleted = 0
    for node in node_names:
        try:
            pods = core_v1.list_namespaced_pod(
                namespace=_MCO_NS,
                label_selector=_MCD_LABEL_SELECTOR,
                field_selector=f"spec.nodeName={node}",
                _request_timeout=_rt(),
            )
        except ApiException as e:
            print(f"  (list MCD pods on {node}: {e.reason or e})", flush=True)
            continue
        for pod in pods.items or []:
            if not pod.metadata or not pod.metadata.name:
                continue
            pname = pod.metadata.name
            print(
                f"  deleting {_MCO_NS}/pod/{pname} (restart MCD on {node})",
                flush=True,
            )
            try:
                core_v1.delete_namespaced_pod(
                    pname,
                    _MCO_NS,
                    _request_timeout=_rt(),
                )
                deleted += 1
            except ApiException as e:
                err = e.reason or e
                print(f"  (delete {pname} failed: {err})", flush=True)
    return deleted


def _machine_config_recovered(
    custom: client.CustomObjectsApi,
) -> bool:
    co = _get_cluster_operator(custom, "machine-config")
    mcp = _get_machine_config_pool(custom, _MCP_MASTER)
    if _co_machine_config_degraded(co):
        return False
    if mcp and _mcp_master_needs_work(mcp):
        return False
    return True


def _wait_machine_config_recovered(
    custom: client.CustomObjectsApi,
    *,
    timeout_sec: int,
    poll_sec: int,
) -> None:
    print(
        f"Waiting for machine-config / master pool to recover "
        f"(up to {timeout_sec}s) ...",
        flush=True,
    )
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        if _machine_config_recovered(custom):
            print(
                "machine-config ClusterOperator and master pool look healthy.",
                flush=True,
            )
            return
        time.sleep(poll_sec)
    raise OperatorConfigError(
        f"Timeout after {timeout_sec}s waiting for machine-config / "
        "master MachineConfigPool to recover after remediation."
    )


class _McRemedyState:
    __slots__ = ("count", "last_ts")

    def __init__(self) -> None:
        self.count = 0
        self.last_ts = 0.0


def _maybe_remediate_machine_config(
    custom: client.CustomObjectsApi,
    core_v1: client.CoreV1Api,
    *,
    state: _McRemedyState,
    wait_timeout_sec: int,
    wait_poll_sec: int,
    skip: bool,
) -> None:
    """If MCO shows currentconfig/master failure, restart MCD pods."""
    if skip:
        return
    now = time.monotonic()
    if state.count >= _MC_REMEDY_MAX_PER_RUN:
        return
    if state.last_ts and (now - state.last_ts) < _MC_REMEDY_COOLDOWN_SEC:
        return

    blob = _machine_config_diagnostic_blob(custom)
    if not _matches_mcd_currentconfig_issue(blob):
        return

    co = _get_cluster_operator(custom, "machine-config")
    mcp = _get_machine_config_pool(custom, _MCP_MASTER)
    if not (
        _co_machine_config_degraded(co) or _mcp_master_needs_work(mcp)
    ):
        return

    nodes = _parse_node_names_from_messages(blob)
    if not nodes:
        nodes = _master_node_names(core_v1)
    if not nodes:
        print(
            "Detected machine-config issue but could not determine node "
            "names; see clusteroperator/machine-config and "
            "machineconfigpool/master.",
            flush=True,
        )
        return

    print(
        "Detected degraded machine-config (missing currentconfig / master "
        "pool not ready). Restarting machine-config-daemon pod(s) ...",
        flush=True,
    )
    n = _delete_mcd_pods_on_nodes(core_v1, nodes)
    if n == 0:
        print(
            "  (no MCD pods deleted; check namespace and labels)",
            flush=True,
        )
        return

    state.count += 1
    state.last_ts = now
    _wait_machine_config_recovered(
        custom,
        timeout_sec=wait_timeout_sec,
        poll_sec=wait_poll_sec,
    )


# Subscriptions that must have an InstallPlan before we proceed to CSV waits.
# Without this gate, approval can run before OLM creates Manual InstallPlans
# and then wait forever for CSVs that never install.
_REQUIRED_INSTALLPLAN_SUBSCRIPTIONS: tuple[tuple[str, str], ...] = (
    ("openshift-storage", "lvms-operator"),
    ("openshift-sriov-network-operator", "sriov-network-operator"),
)


def _list_all_installplans(
    custom: client.CustomObjectsApi,
) -> list[dict[str, Any]]:
    """Cluster-scoped list of InstallPlans (all namespaces)."""
    try:
        data = custom.list_cluster_custom_object(
            group=_OLM_GROUP,
            version=_OLM_VERSION,
            plural=_PLURAL_INSTALLPLAN,
            _request_timeout=_rt(),
        )
    except ApiException as e:
        raise OperatorConfigError(
            f"list installplans (cluster) failed: {e.reason or e}"
        ) from e
    if not isinstance(data, dict):
        return []
    items = data.get("items", [])
    return [i for i in items if isinstance(i, dict)]


def _list_unapproved_installplans(
    custom: client.CustomObjectsApi,
    core_v1: client.CoreV1Api | None = None,
) -> list[tuple[str, str]]:
    """Return (namespace, name) for InstallPlans with approved == false."""
    del core_v1  # kept for call-site compatibility
    out: list[tuple[str, str]] = []
    for item in _list_all_installplans(custom):
        meta = item.get("metadata") or {}
        name = meta.get("name")
        ns = meta.get("namespace")
        spec = item.get("spec") or {}
        if spec.get("approved") is not False:
            continue
        if not isinstance(name, str) or not isinstance(ns, str):
            continue
        out.append((ns, name))
    return out


def _subscription_has_installplan(
    custom: client.CustomObjectsApi,
    namespace: str,
    name: str,
) -> bool:
    try:
        sub = custom.get_namespaced_custom_object(
            group=_OLM_GROUP,
            version=_OLM_VERSION,
            namespace=namespace,
            plural=_PLURAL_SUBSCRIPTION,
            name=name,
            _request_timeout=_rt(),
        )
    except ApiException as e:
        if e.status in (403, 404):
            return False
        raise OperatorConfigError(
            f"get subscription/{name} in {namespace} failed: {e.reason or e}"
        ) from e
    if not isinstance(sub, dict):
        return False
    status = sub.get("status") or {}
    if status.get("installPlanRef") or status.get("installplan"):
        return True
    # Fallback: any InstallPlan in the namespace naming this subscription's CSV
    current_csv = status.get("currentCSV") or ""
    for item in _list_all_installplans(custom):
        meta = item.get("metadata") or {}
        if meta.get("namespace") != namespace:
            continue
        spec = item.get("spec") or {}
        csvs = spec.get("clusterServiceVersionNames") or []
        if current_csv and current_csv in csvs:
            return True
        if not current_csv and csvs:
            # Subscription exists and namespace has an IP — good enough
            return True
    return False


def _wait_for_required_installplans(
    custom: client.CustomObjectsApi,
    *,
    timeout_sec: int,
    poll_sec: int,
) -> None:
    """Block until LVMS/SR-IOV Subscriptions have InstallPlans (or timeout)."""
    print(
        f"Waiting for required Subscriptions to produce InstallPlans "
        f"(up to {timeout_sec}s) ...",
        flush=True,
    )
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        missing: list[str] = []
        for ns, name in _REQUIRED_INSTALLPLAN_SUBSCRIPTIONS:
            if _subscription_has_installplan(custom, ns, name):
                continue
            missing.append(f"{ns}/{name}")
        if not missing:
            print("Required InstallPlans are present.", flush=True)
            return
        print(
            f"  still waiting for InstallPlan(s): {', '.join(missing)}",
            flush=True,
        )
        time.sleep(poll_sec)
    raise OperatorConfigError(
        f"Timeout after {timeout_sec}s waiting for InstallPlans from "
        f"{_REQUIRED_INSTALLPLAN_SUBSCRIPTIONS}."
    )


def _approve_pending_installplans(
    custom: client.CustomObjectsApi,
    core_v1: client.CoreV1Api | None = None,
    *,
    quiet_if_none: bool = False,
) -> int:
    """Merge-patch approved: true on each unapproved InstallPlan.

    Returns the number of InstallPlans approved in this call.
    """
    del core_v1  # kept for call-site compatibility
    pending = _list_unapproved_installplans(custom)
    if not pending:
        if not quiet_if_none:
            print("No unapproved InstallPlans (nothing to approve).", flush=True)
        return 0
    print(f"Approving {len(pending)} pending InstallPlan(s) ...", flush=True)
    patch_body: dict[str, Any] = {"spec": {"approved": True}}
    for ns, ip_name in pending:
        print(f"  approve installplan/{ip_name} in {ns}", flush=True)
        try:
            custom.patch_namespaced_custom_object(
                group=_OLM_GROUP,
                version=_OLM_VERSION,
                namespace=ns,
                plural=_PLURAL_INSTALLPLAN,
                name=ip_name,
                body=patch_body,
                _content_type="application/merge-patch+json",
                _request_timeout=_rt(),
            )
        except ApiException as e:
            raise OperatorConfigError(
                f"patch installplan/{ip_name} in {ns} failed: {e.reason or e}"
            ) from e
    print("InstallPlan approval complete.", flush=True)
    return len(pending)


def _event_sort_key(ev: Any) -> datetime:
    t = getattr(ev, "last_timestamp", None) or getattr(ev, "event_time", None)
    if t is None:
        return datetime.min.replace(tzinfo=timezone.utc)
    if t.tzinfo is None:
        return t.replace(tzinfo=timezone.utc)
    return t


def _print_csv_timeout_diagnostics(
    namespace: str,
    *,
    core_v1: client.CoreV1Api,
    custom: client.CustomObjectsApi,
) -> None:
    for label, plural in (
        ("subscription", _PLURAL_SUBSCRIPTION),
        ("csv", _PLURAL_CSV),
    ):
        print(f"--- {label} ({namespace}) ---", flush=True)
        try:
            data = custom.list_namespaced_custom_object(
                group=_OLM_GROUP,
                version=_OLM_VERSION,
                namespace=namespace,
                plural=plural,
                _request_timeout=_rt(),
            )
        except ApiException as e:
            print(f"  (list {label} failed: {e.reason or e})", flush=True)
            continue
        items = data.get("items", []) if isinstance(data, dict) else []
        for item in items:
            meta = item.get("metadata") or {}
            name = meta.get("name", "")
            status = item.get("status") or {}
            phase = status.get("phase", "")
            print(f"  {name}\t{phase}", flush=True)

    print(f"--- pods ({namespace}) ---", flush=True)
    try:
        pods = core_v1.list_namespaced_pod(namespace, _request_timeout=_rt())
    except ApiException as e:
        print(f"  (list pods failed: {e.reason or e})", flush=True)
    else:
        for pod in pods.items or []:
            if not pod.metadata:
                continue
            phase = pod.status.phase if pod.status else ""
            print(f"  {pod.metadata.name}\t{phase}", flush=True)

    print("Recent events:", flush=True)
    try:
        ev_list = core_v1.list_namespaced_event(
            namespace,
            _request_timeout=_rt(),
        )
    except ApiException as e:
        print(f"  (list events failed: {e.reason or e})", flush=True)
        return
    items = sorted(ev_list.items or [], key=_event_sort_key)
    tail = items[-40:] if len(items) > 40 else items
    for ev in tail:
        inv = ev.involved_object
        src = (inv.kind if inv else "") + "/" + (inv.name if inv else "")
        msg = (ev.message or "").replace("\n", " ")[:120]
        ts = ev.last_timestamp or ev.first_timestamp
        print(f"  {ts}\t{src}\t{ev.type}\t{msg}", flush=True)


def _wait_csv_succeeded(
    namespace: str,
    name_prefix: str,
    description: str,
    *,
    custom: client.CustomObjectsApi,
    core_v1: client.CoreV1Api,
    timeout_sec: int,
    poll_sec: int,
    mc_state: _McRemedyState | None,
    mc_wait_timeout_sec: int,
    mc_wait_poll_sec: int,
    skip_mc_remediation: bool,
) -> None:
    msg = (
        f"Waiting for {description} CSV (name prefix {name_prefix}) "
        f"in {namespace} (up to {timeout_sec}s) ..."
    )
    print(msg, flush=True)
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        # Late Manual InstallPlans (created after the initial approval pass)
        # must still be approved or CSVs never leave Pending.
        _approve_pending_installplans(custom, quiet_if_none=True)
        if mc_state is not None:
            _maybe_remediate_machine_config(
                custom,
                core_v1,
                state=mc_state,
                wait_timeout_sec=mc_wait_timeout_sec,
                wait_poll_sec=mc_wait_poll_sec,
                skip=skip_mc_remediation,
            )
        try:
            data = custom.list_namespaced_custom_object(
                group=_OLM_GROUP,
                version=_OLM_VERSION,
                namespace=namespace,
                plural=_PLURAL_CSV,
                _request_timeout=_rt(),
            )
        except ApiException as e:
            print(f"  (list csv failed: {e.reason or e})", flush=True)
            time.sleep(poll_sec)
            continue
        if not isinstance(data, dict):
            time.sleep(poll_sec)
            continue
        matched = False
        for item in data.get("items", []):
            meta = item.get("metadata") or {}
            name = meta.get("name")
            if not isinstance(name, str) or not name.startswith(name_prefix):
                continue
            matched = True
            status = item.get("status") or {}
            phase = status.get("phase") or ""
            print(f"  {name}: {phase or 'unknown'}", flush=True)
            if phase == "Succeeded":
                print(f"  {description} ready.", flush=True)
                return
            break
        if not matched:
            print(
                f"  (no CSV with prefix {name_prefix} in {namespace} yet)",
                flush=True,
            )
        time.sleep(poll_sec)

    print(f"Timeout waiting for {description} CSV in {namespace}.", flush=True)
    _print_csv_timeout_diagnostics(namespace, core_v1=core_v1, custom=custom)
    err = (
        f"Timeout waiting for {description} CSV in {namespace} "
        f"after {timeout_sec}s."
    )
    raise OperatorConfigError(err)


def _wait_required_operator_csvs(
    *,
    custom: client.CustomObjectsApi,
    core_v1: client.CoreV1Api,
    csv_timeout_sec: int,
    csv_poll_sec: int,
    mc_state: _McRemedyState | None,
    mc_wait_timeout_sec: int,
    mc_wait_poll_sec: int,
    skip_mc_remediation: bool,
) -> None:
    targets: tuple[tuple[str, str, str], ...] = (
        ("openshift-storage", "lvms-operator", "LVMS"),
        (
            "openshift-sriov-network-operator",
            "sriov-network-operator",
            "SR-IOV",
        ),
    )
    for ns, prefix, desc in targets:
        _wait_csv_succeeded(
            ns,
            prefix,
            desc,
            custom=custom,
            core_v1=core_v1,
            timeout_sec=csv_timeout_sec,
            poll_sec=csv_poll_sec,
            mc_state=mc_state,
            mc_wait_timeout_sec=mc_wait_timeout_sec,
            mc_wait_poll_sec=mc_wait_poll_sec,
            skip_mc_remediation=skip_mc_remediation,
        )
    print("All target operators report CSV Succeeded.", flush=True)


def _print_crd_timeout_hints(
    crd_name: str,
    *,
    custom: client.CustomObjectsApi,
) -> None:
    """After CRD wait failure, print OLM/operator state for common cases."""
    hints: list[tuple[str, str]] = []
    if "lvm" in crd_name.lower():
        hints.append(("openshift-storage (LVMS)", "openshift-storage"))
    if "sriov" in crd_name.lower():
        hints.append(
            (
                "openshift-sriov-network-operator",
                "openshift-sriov-network-operator",
            )
        )
    for title, ns in hints:
        print(f"Hint — {title}:", flush=True)
        try:
            data = custom.list_namespaced_custom_object(
                group=_OLM_GROUP,
                version=_OLM_VERSION,
                namespace=ns,
                plural=_PLURAL_SUBSCRIPTION,
                _request_timeout=_rt(),
            )
        except ApiException as e:
            print(f"  (list subscription failed: {e.reason or e})", flush=True)
            continue
        items = data.get("items", []) if isinstance(data, dict) else []
        for item in items:
            meta = item.get("metadata") or {}
            st = item.get("status") or {}
            print(
                f"  {meta.get('name', '')}\t{st.get('state', '')}",
                flush=True,
            )


def _crd_has_established(
    ext: client.ApiextensionsV1Api,
    crd_name: str,
) -> bool:
    try:
        crd = ext.read_custom_resource_definition(
            crd_name, _request_timeout=_rt()
        )
    except ApiException:
        return False
    for c in crd.status.conditions or []:
        if c.type == "Established" and c.status == "True":
            return True
    return False


def _wait_crd(
    crd_name: str,
    *,
    ext: client.ApiextensionsV1Api,
    custom: client.CustomObjectsApi,
    timeout_sec: int,
    poll_sec: int = 5,
) -> None:
    print(f"Waiting for CRD {crd_name} (up to {timeout_sec}s) ...", flush=True)
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        try:
            ext.read_custom_resource_definition(
                crd_name,
                _request_timeout=_rt(),
            )
        except ApiException as e:
            if e.status != 404:
                print(f"  (get crd failed: {e.reason or e})", flush=True)
            time.sleep(poll_sec)
            continue

        est_deadline = time.monotonic() + 120
        while time.monotonic() < est_deadline:
            if _crd_has_established(ext, crd_name):
                print(f"CRD {crd_name} available.", flush=True)
                return
            time.sleep(2)

        note = (
            f"Note: Established condition not True for {crd_name} "
            "within 120s; continuing."
        )
        print(note, flush=True)
        print(f"CRD {crd_name} available.", flush=True)
        return

    print("Relevant CRDs (lvm/sriov):", flush=True)
    try:
        lst = ext.list_custom_resource_definition(_request_timeout=_rt())
    except ApiException as e:
        print(f"  (list crd failed: {e.reason or e})", flush=True)
    else:
        for crd in lst.items or []:
            if not crd.metadata or not crd.metadata.name:
                continue
            n = crd.metadata.name
            if "lvm" in n.lower() or "sriov" in n.lower():
                print(f"  {n}", flush=True)
    _print_crd_timeout_hints(crd_name, custom=custom)
    raise OperatorConfigError(
        f"Timeout waiting for CRD {crd_name} after {timeout_sec}s."
    )


_SERVER_MANAGED_META_KEYS = (
    "creationTimestamp",
    "resourceVersion",
    "uid",
    "generation",
    "managedFields",
    "selfLink",
)


def _iter_yaml_docs(path: Path) -> list[dict[str, Any]]:
    """Load multi-doc YAML; skip empty documents (e.g. leading ---)."""
    with path.open(encoding="utf-8") as fh:
        docs = list(yaml.safe_load_all(fh))
    out: list[dict[str, Any]] = []
    for doc in docs:
        if isinstance(doc, dict) and doc.get("kind"):
            out.append(doc)
    return out


def _sanitize_for_apply(doc: dict[str, Any]) -> dict[str, Any]:
    """Drop cluster-instance metadata so re-apply works across reinstalls.

    Exported ConfigMaps (e.g. supported-nic-ids) often retain uid /
    resourceVersion / ownerReferences from a previous cluster; server-side
    apply then fails with uid mismatch.
    """
    clean = dict(doc)
    meta = dict(clean.get("metadata") or {})
    for key in _SERVER_MANAGED_META_KEYS:
        meta.pop(key, None)
    # OLM will re-own operator-managed CMs; keep labels/annotations/name/ns.
    meta.pop("ownerReferences", None)
    clean["metadata"] = meta
    clean.pop("status", None)
    return clean


def _ensure_sriov_capable_label(
    core_v1: client.CoreV1Api,
    node_name: str,
) -> None:
    """Add network-sriov.capable via merge-patch (does not wipe other labels)."""
    label = "feature.node.kubernetes.io/network-sriov.capable"
    print(f"Ensuring node/{node_name} has label {label}=true ...", flush=True)
    try:
        core_v1.patch_node(
            node_name,
            {"metadata": {"labels": {label: "true"}}},
            _request_timeout=_rt(),
        )
    except ApiException as e:
        raise OperatorConfigError(
            f"patch node/{node_name} label failed: {e.reason or e}"
        ) from e
    print(f"  node/{node_name} labeled.", flush=True)


def _apply_file(path: Path, *, api_client: client.ApiClient) -> None:
    """Server-side apply each document using the object's metadata.namespace.

    kubernetes.utils.create_from_yaml(..., apply=True) defaults namespace to
    ``default`` and passes that into DynamicClient.server_side_apply even when
    the object declares another namespace — the API then returns BadRequest
    ("namespace of the provided object does not match the namespace sent on
    the request"). Non-apply create_from_yaml avoids this by reading metadata.
    """
    if not path.is_file():
        raise OperatorConfigError(f"Manifest not found: {path}")
    root = _repo_root()
    try:
        display = str(path.relative_to(root))
    except ValueError:
        display = str(path)
    print(f"Applying {display} ...", flush=True)
    docs = _iter_yaml_docs(path)
    if not docs:
        raise OperatorConfigError(f"No Kubernetes objects found in {display}")
    try:
        for doc in docs:
            body = _sanitize_for_apply(doc)
            meta = body.get("metadata") or {}
            # Cluster-scoped objects omit namespace; still pass a placeholder
            # that create_from_dict will drop for non-namespaced kinds on the
            # create path — for apply=True the DynamicClient uses body ns.
            ns = meta.get("namespace") or "default"
            create_from_dict(
                api_client,
                body,
                verbose=False,
                apply=True,
                namespace=ns,
                # Take ownership when the object was previously applied by
                # kubectl/oc (field manager conflict on .data / .spec).
                force_conflicts=True,
            )
    except FailToCreateError as e:
        raise OperatorConfigError(
            f"apply failed for {display}: {e}"
        ) from e
    print(f"  applied {len(docs)} object(s) from {display}", flush=True)


def apply_operator_config(
    *,
    kubeconfig: Path,
    csv_timeout_sec: int = 1800,
    csv_poll_sec: int = 15,
    crd_timeout_sec: int = 900,
    mc_remediate_timeout_sec: int = 3600,
    mc_remediate_poll_sec: int = 15,
    skip_mc_remediation: bool = False,
    installplan_wait_sec: int = 600,
    installplan_poll_sec: int = 10,
) -> None:
    d = _operator_config_dir()
    if not d.is_dir():
        raise OperatorConfigError(f"Directory not found: {d}")

    api_client, core_v1, custom, ext = _load_clients(kubeconfig)

    mc_state = _McRemedyState()
    _maybe_remediate_machine_config(
        custom,
        core_v1,
        state=mc_state,
        wait_timeout_sec=mc_remediate_timeout_sec,
        wait_poll_sec=mc_remediate_poll_sec,
        skip=skip_mc_remediation,
    )

    _apply_file(d / "monitoring-config-cm.yaml", api_client=api_client)
    _apply_file(d / "supported-nic-ids.yaml", api_client=api_client)

    _wait_for_required_installplans(
        custom,
        timeout_sec=installplan_wait_sec,
        poll_sec=installplan_poll_sec,
    )
    _approve_pending_installplans(custom)

    _wait_required_operator_csvs(
        custom=custom,
        core_v1=core_v1,
        csv_timeout_sec=csv_timeout_sec,
        csv_poll_sec=csv_poll_sec,
        mc_state=mc_state,
        mc_wait_timeout_sec=mc_remediate_timeout_sec,
        mc_wait_poll_sec=mc_remediate_poll_sec,
        skip_mc_remediation=skip_mc_remediation,
    )

    _wait_crd(
        "lvmclusters.lvm.topolvm.io",
        ext=ext,
        custom=custom,
        timeout_sec=crd_timeout_sec,
    )
    _apply_file(d / "lvms-lvm-cluster.yaml", api_client=api_client)

    _wait_crd(
        "sriovoperatorconfigs.sriovnetwork.openshift.io",
        ext=ext,
        custom=custom,
        timeout_sec=crd_timeout_sec,
    )
    _wait_crd(
        "sriovnetworknodepolicies.sriovnetwork.openshift.io",
        ext=ext,
        custom=custom,
        timeout_sec=crd_timeout_sec,
    )
    # Patch-only: never replace the Node object (that wipes role labels).
    _ensure_sriov_capable_label(core_v1, "master-0")
    sriov_manifest = d / "sriov-config-netdevice-eno2np1.yaml"
    _apply_file(sriov_manifest, api_client=api_client)

    # Catch InstallPlans from slower AllNamespaces operators (GitOps/RHOAI/SNR).
    _approve_pending_installplans(custom, quiet_if_none=True)

    print("operator-config apply complete.", flush=True)


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        description=(
            "Apply operator-config manifests: remediate stuck machine-config "
            "if needed, wait for and approve pending InstallPlans, wait for "
            "LVMS/SR-IOV CSVs, then for CRDs as needed."
        ),
    )
    p.add_argument(
        "--kubeconfig",
        help="Path to kubeconfig (overrides KUBECONFIG / KUBECONFIG_PATH).",
    )
    p.add_argument(
        "--csv-timeout",
        type=int,
        default=_env_int("WAIT_CSV_TIMEOUT_SEC", 1800),
        metavar="SEC",
        help=(
            "Seconds to wait for each operator CSV "
            "(default: 1800, or WAIT_CSV_TIMEOUT_SEC)."
        ),
    )
    p.add_argument(
        "--csv-poll",
        type=int,
        default=_env_int("WAIT_CSV_POLL_SEC", 15),
        metavar="SEC",
        help=(
            "Poll interval while waiting for CSVs "
            "(default: 15, or WAIT_CSV_POLL_SEC)."
        ),
    )
    p.add_argument(
        "--crd-timeout",
        type=int,
        default=900,
        metavar="SEC",
        help="Seconds to wait for each CRD (default: 900).",
    )
    p.add_argument(
        "--installplan-wait",
        type=int,
        default=_env_int("WAIT_INSTALLPLAN_TIMEOUT_SEC", 600),
        metavar="SEC",
        help=(
            "Seconds to wait for LVMS/SR-IOV Subscriptions to create "
            "InstallPlans before approval "
            "(default: 600, or WAIT_INSTALLPLAN_TIMEOUT_SEC)."
        ),
    )
    p.add_argument(
        "--installplan-poll",
        type=int,
        default=_env_int("WAIT_INSTALLPLAN_POLL_SEC", 10),
        metavar="SEC",
        help=(
            "Poll interval while waiting for InstallPlans "
            "(default: 10, or WAIT_INSTALLPLAN_POLL_SEC)."
        ),
    )
    p.add_argument(
        "--mc-remediate-timeout",
        type=int,
        default=_env_int("MC_REMEDIATE_TIMEOUT_SEC", 3600),
        metavar="SEC",
        help=(
            "Seconds to wait for machine-config / master pool after MCD pod "
            "restart (default: 3600, or MC_REMEDIATE_TIMEOUT_SEC)."
        ),
    )
    p.add_argument(
        "--mc-remediate-poll",
        type=int,
        default=15,
        metavar="SEC",
        help="Poll interval during MCO recovery wait (default: 15).",
    )
    p.add_argument(
        "--skip-mc-remediation",
        action="store_true",
        help=(
            "Do not restart machine-config-daemon pods when the master pool "
            "is degraded with missing currentconfig."
        ),
    )
    p.add_argument(
        "--approve-only",
        action="store_true",
        help=(
            "Only approve pending Manual InstallPlans and exit "
            "(used after applying remaining operator-install manifests)."
        ),
    )
    args = p.parse_args(argv)

    try:
        kc = _resolve_kubeconfig(args.kubeconfig)
        if args.approve_only:
            _api_client, _core_v1, custom, _ext = _load_clients(kc)
            n = _approve_pending_installplans(custom)
            print(f"approve-only done ({n} InstallPlan(s)).", flush=True)
            return 0
        apply_operator_config(
            kubeconfig=kc,
            csv_timeout_sec=args.csv_timeout,
            csv_poll_sec=args.csv_poll,
            crd_timeout_sec=args.crd_timeout,
            mc_remediate_timeout_sec=args.mc_remediate_timeout,
            mc_remediate_poll_sec=args.mc_remediate_poll,
            skip_mc_remediation=args.skip_mc_remediation,
            installplan_wait_sec=args.installplan_wait,
            installplan_poll_sec=args.installplan_poll,
        )
    except OperatorConfigError as e:
        print(f"Error: {e}", file=sys.stderr, flush=True)
        return 1
    except KeyboardInterrupt:
        print("Interrupted.", file=sys.stderr, flush=True)
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
