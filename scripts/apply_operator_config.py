#!/usr/bin/env python3
"""Apply abi-master-0/extra-manifests/operator-config in a safe order.

1. ConfigMaps (no CRDs).
2. Approve cluster-wide InstallPlans still pending (Manual approval).
3. Wait for LVMS and SR-IOV ClusterServiceVersions (phase Succeeded).
4. Wait for LVMS CRD, apply LVMCluster.
5. Wait for SR-IOV CRDs, apply Sriov resources.

Uses the Kubernetes Python client (``kubernetes`` package) with kubeconfig
authentication. Set ``KUBECONFIG`` or ``KUBECONFIG_PATH`` to the cluster
kubeconfig file.
"""

from __future__ import annotations

import argparse
import os
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

try:
    from kubernetes import client, config as k8s_config
    from kubernetes.client.exceptions import ApiException
    from kubernetes.utils import create_from_yaml
    from kubernetes.utils.create_from_yaml import FailToCreateError
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


def _iter_namespace_names(core_v1: client.CoreV1Api) -> list[str]:
    names: list[str] = []
    continue_token: str | None = None
    while True:
        kwargs: dict[str, Any] = {"limit": 500, "_request_timeout": _rt()}
        if continue_token:
            kwargs["_continue"] = continue_token
        resp = core_v1.list_namespace(**kwargs)
        for ns in resp.items or []:
            if ns.metadata and ns.metadata.name:
                names.append(ns.metadata.name)
        continue_token = resp.metadata._continue if resp.metadata else None
        if not continue_token:
            break
    return names


def _list_unapproved_installplans(
    custom: client.CustomObjectsApi,
    core_v1: client.CoreV1Api,
) -> list[tuple[str, str]]:
    """Return (namespace, name) for InstallPlans with approved == false."""
    out: list[tuple[str, str]] = []
    for ns in _iter_namespace_names(core_v1):
        try:
            data = custom.list_namespaced_custom_object(
                group=_OLM_GROUP,
                version=_OLM_VERSION,
                namespace=ns,
                plural=_PLURAL_INSTALLPLAN,
                _request_timeout=_rt(),
            )
        except ApiException as e:
            if e.status == 403:
                continue
            if e.status == 404:
                continue
            raise OperatorConfigError(
                f"list installplans in {ns} failed: {e.reason or e}"
            ) from e
        if not isinstance(data, dict):
            continue
        for item in data.get("items", []):
            meta = item.get("metadata") or {}
            name = meta.get("name")
            spec = item.get("spec") or {}
            if spec.get("approved") is not False:
                continue
            if not isinstance(name, str):
                continue
            out.append((ns, name))
    return out


def _approve_pending_installplans(
    custom: client.CustomObjectsApi,
    core_v1: client.CoreV1Api,
) -> None:
    """Merge-patch approved: true on each unapproved InstallPlan."""
    pending = _list_unapproved_installplans(custom, core_v1)
    if not pending:
        print("No unapproved InstallPlans (nothing to approve).", flush=True)
        return
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
) -> None:
    msg = (
        f"Waiting for {description} CSV (name prefix {name_prefix}) "
        f"in {namespace} (up to {timeout_sec}s) ..."
    )
    print(msg, flush=True)
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
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


def _apply_file(path: Path, *, api_client: client.ApiClient) -> None:
    if not path.is_file():
        raise OperatorConfigError(f"Manifest not found: {path}")
    root = _repo_root()
    try:
        display = str(path.relative_to(root))
    except ValueError:
        display = str(path)
    print(f"Applying {display} ...", flush=True)
    try:
        create_from_yaml(
            api_client,
            str(path),
            verbose=False,
            apply=True,
        )
    except FailToCreateError as e:
        raise OperatorConfigError(
            f"apply failed for {display}: {e}"
        ) from e


def apply_operator_config(
    *,
    kubeconfig: Path,
    csv_timeout_sec: int = 1800,
    csv_poll_sec: int = 15,
    crd_timeout_sec: int = 900,
) -> None:
    d = _operator_config_dir()
    if not d.is_dir():
        raise OperatorConfigError(f"Directory not found: {d}")

    api_client, core_v1, custom, ext = _load_clients(kubeconfig)

    _apply_file(d / "monitoring-config-cm.yaml", api_client=api_client)
    _apply_file(d / "supported-nic-ids.yaml", api_client=api_client)

    _approve_pending_installplans(custom, core_v1)

    _wait_required_operator_csvs(
        custom=custom,
        core_v1=core_v1,
        csv_timeout_sec=csv_timeout_sec,
        csv_poll_sec=csv_poll_sec,
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
    sriov_manifest = d / "sriov-config-netdevice-eno2np1.yaml"
    _apply_file(sriov_manifest, api_client=api_client)

    print("operator-config apply complete.", flush=True)


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        description=(
            "Apply operator-config manifests: approve pending InstallPlans, "
            "wait for LVMS/SR-IOV CSVs, then for CRDs as needed."
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
    args = p.parse_args(argv)

    try:
        kc = _resolve_kubeconfig(args.kubeconfig)
        apply_operator_config(
            kubeconfig=kc,
            csv_timeout_sec=args.csv_timeout,
            csv_poll_sec=args.csv_poll,
            crd_timeout_sec=args.crd_timeout,
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
