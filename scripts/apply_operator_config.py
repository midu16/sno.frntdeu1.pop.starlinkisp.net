#!/usr/bin/env python3
"""Apply abi-master-0/extra-manifests/operator-config in a safe order.

1. ConfigMaps (no CRDs).
2. Wait for LVMS and SR-IOV ClusterServiceVersions (phase Succeeded).
3. Wait for LVMS CRD, apply LVMCluster.
4. Wait for SR-IOV CRDs, apply Sriov resources.

Requires ``oc`` on PATH. Set ``KUBECONFIG`` or ``KUBECONFIG_PATH`` to the cluster kubeconfig file.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path


class OperatorConfigError(Exception):
    """Fatal error applying operator-config (exit code 1)."""


def _repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def _operator_config_dir() -> Path:
    return _repo_root() / "abi-master-0" / "extra-manifests" / "operator-config"


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


def _oc_env(kubeconfig: Path) -> dict[str, str]:
    env = os.environ.copy()
    env["KUBECONFIG"] = str(kubeconfig)
    return env


def _run_oc(
    args: list[str],
    *,
    kubeconfig: Path,
    check: bool = True,
) -> subprocess.CompletedProcess[str]:
    cmd = ["oc", *args]
    try:
        return subprocess.run(
            cmd,
            env=_oc_env(kubeconfig),
            check=check,
            capture_output=True,
            text=True,
            timeout=600,
        )
    except FileNotFoundError as e:
        raise OperatorConfigError(
            "'oc' not found on PATH; install OpenShift CLI client."
        ) from e
    except subprocess.TimeoutExpired as e:
        raise OperatorConfigError(f"oc command timed out: {' '.join(cmd)}") from e


def _oc_checked(args: list[str], *, kubeconfig: Path) -> None:
    proc = _run_oc(args, kubeconfig=kubeconfig, check=False)
    if proc.returncode != 0:
        err = proc.stderr.strip() or proc.stdout.strip() or f"exit {proc.returncode}"
        raise OperatorConfigError(f"oc {' '.join(args)} failed: {err}")


def _env_int(name: str, default: int) -> int:
    raw = os.environ.get(name)
    if raw is None or not str(raw).strip():
        return default
    try:
        return int(str(raw).strip())
    except ValueError:
        return default


def _print_csv_timeout_diagnostics(namespace: str, *, kubeconfig: Path) -> None:
    proc = _run_oc(
        ["get", "subscription,csv,pods", "-n", namespace, "-o", "wide"],
        kubeconfig=kubeconfig,
        check=False,
    )
    print(proc.stdout.rstrip() if proc.stdout else "", flush=True)
    if proc.stderr:
        print(proc.stderr.rstrip(), file=sys.stderr, flush=True)
    ev = _run_oc(
        ["get", "events", "-n", namespace, "--sort-by=.lastTimestamp"],
        kubeconfig=kubeconfig,
        check=False,
    )
    if ev.returncode == 0 and ev.stdout:
        lines = ev.stdout.splitlines()
        tail = lines[-40:] if len(lines) > 40 else lines
        print("Recent events:", flush=True)
        for line in tail:
            print(line, flush=True)


def _wait_csv_succeeded(
    namespace: str,
    name_prefix: str,
    description: str,
    *,
    kubeconfig: Path,
    timeout_sec: int,
    poll_sec: int,
) -> None:
    print(
        f"Waiting for {description} CSV (name prefix {name_prefix}) in {namespace} "
        f"(up to {timeout_sec}s) ...",
        flush=True,
    )
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        proc = _run_oc(
            ["get", "csv", "-n", namespace, "-o", "json"],
            kubeconfig=kubeconfig,
            check=False,
        )
        if proc.returncode != 0:
            err = proc.stderr.strip() or str(proc.returncode)
            print(f"  (oc get csv failed: {err})", flush=True)
            time.sleep(poll_sec)
            continue
        try:
            data = json.loads(proc.stdout)
        except json.JSONDecodeError:
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
            print(f"  (no CSV with prefix {name_prefix} in {namespace} yet)", flush=True)
        time.sleep(poll_sec)

    print(f"Timeout waiting for {description} CSV in {namespace}.", flush=True)
    _print_csv_timeout_diagnostics(namespace, kubeconfig=kubeconfig)
    raise OperatorConfigError(
        f"Timeout waiting for {description} CSV in {namespace} after {timeout_sec}s."
    )


def _wait_required_operator_csvs(
    *,
    kubeconfig: Path,
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
            kubeconfig=kubeconfig,
            timeout_sec=csv_timeout_sec,
            poll_sec=csv_poll_sec,
        )
    print("All target operators report CSV Succeeded.", flush=True)


def _print_crd_timeout_hints(crd_name: str, *, kubeconfig: Path) -> None:
    """After CRD wait failure, print OLM/operator state for common cases."""
    hints: list[tuple[str, list[str]]] = []
    if "lvm" in crd_name.lower():
        hints.append(
            (
                "openshift-storage (LVMS)",
                [
                    "get",
                    "subscription,csv,pods",
                    "-n",
                    "openshift-storage",
                    "-o",
                    "wide",
                ],
            )
        )
    if "sriov" in crd_name.lower():
        hints.append(
            (
                "openshift-sriov-network-operator",
                [
                    "get",
                    "subscription,csv,pods",
                    "-n",
                    "openshift-sriov-network-operator",
                    "-o",
                    "wide",
                ],
            )
        )
    for title, oc_args in hints:
        print(f"Hint — {title}:", flush=True)
        proc = _run_oc(oc_args, kubeconfig=kubeconfig, check=False)
        if proc.returncode == 0 and proc.stdout.strip():
            for line in proc.stdout.splitlines():
                print(f"  {line}", flush=True)
        else:
            err = proc.stderr.strip() if proc.stderr else ""
            print(f"  (oc failed: {err or proc.returncode})", flush=True)


def _wait_crd(
    crd_name: str,
    *,
    kubeconfig: Path,
    timeout_sec: int,
    poll_sec: int = 5,
) -> None:
    print(f"Waiting for CRD {crd_name} (up to {timeout_sec}s) ...", flush=True)
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        proc = _run_oc(
            ["get", "crd", crd_name],
            kubeconfig=kubeconfig,
            check=False,
        )
        if proc.returncode == 0:
            wait_proc = _run_oc(
                ["wait", "--for=condition=Established", f"crd/{crd_name}", "--timeout=120s"],
                kubeconfig=kubeconfig,
                check=False,
            )
            if wait_proc.returncode != 0:
                print(
                    f"Note: oc wait Established for {crd_name} returned {wait_proc.returncode}; continuing.",
                    flush=True,
                )
            print(f"CRD {crd_name} available.", flush=True)
            return
        time.sleep(poll_sec)

    print("Relevant CRDs (lvm/sriov):", flush=True)
    proc = _run_oc(["get", "crd"], kubeconfig=kubeconfig, check=False)
    if proc.returncode == 0:
        for line in proc.stdout.splitlines():
            if "lvm" in line.lower() or "sriov" in line.lower():
                print(f"  {line}", flush=True)
    _print_crd_timeout_hints(crd_name, kubeconfig=kubeconfig)
    raise OperatorConfigError(f"Timeout waiting for CRD {crd_name} after {timeout_sec}s.")


def _apply_file(path: Path, *, kubeconfig: Path) -> None:
    if not path.is_file():
        raise OperatorConfigError(f"Manifest not found: {path}")
    root = _repo_root()
    try:
        display = str(path.relative_to(root))
    except ValueError:
        display = str(path)
    print(f"Applying {display} ...", flush=True)
    _oc_checked(["apply", "-f", str(path)], kubeconfig=kubeconfig)


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

    _apply_file(d / "monitoring-config-cm.yaml", kubeconfig=kubeconfig)
    _apply_file(d / "supported-nic-ids.yaml", kubeconfig=kubeconfig)

    _wait_required_operator_csvs(
        kubeconfig=kubeconfig,
        csv_timeout_sec=csv_timeout_sec,
        csv_poll_sec=csv_poll_sec,
    )

    _wait_crd(
        "lvmclusters.lvm.topolvm.io",
        kubeconfig=kubeconfig,
        timeout_sec=crd_timeout_sec,
    )
    _apply_file(d / "lvms-lvm-cluster.yaml", kubeconfig=kubeconfig)

    _wait_crd(
        "sriovoperatorconfigs.sriovnetwork.openshift.io",
        kubeconfig=kubeconfig,
        timeout_sec=crd_timeout_sec,
    )
    _wait_crd(
        "sriovnetworknodepolicies.sriovnetwork.openshift.io",
        kubeconfig=kubeconfig,
        timeout_sec=crd_timeout_sec,
    )
    _apply_file(d / "sriov-config-netdevice-eno2np1.yaml", kubeconfig=kubeconfig)

    print("operator-config apply complete.", flush=True)


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        description=(
            "Apply operator-config manifests: wait for LVMS/SR-IOV CSVs, then for CRDs as needed."
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
        help="Seconds to wait for each operator CSV (default: 1800, or WAIT_CSV_TIMEOUT_SEC).",
    )
    p.add_argument(
        "--csv-poll",
        type=int,
        default=_env_int("WAIT_CSV_POLL_SEC", 15),
        metavar="SEC",
        help="Poll interval while waiting for CSVs (default: 15, or WAIT_CSV_POLL_SEC).",
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
