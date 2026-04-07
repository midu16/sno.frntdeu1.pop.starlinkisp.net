#!/usr/bin/env python3
"""Apply abi-master-0/extra-manifests/operator-config in a safe order.

1. ConfigMaps (no CRDs).
2. Wait for LVMS CRD, apply LVMCluster.
3. Wait for SR-IOV CRDs, apply Sriov resources.

Requires ``oc`` on PATH. Set ``KUBECONFIG`` or ``KUBECONFIG_PATH`` to the cluster kubeconfig file.
"""

from __future__ import annotations

import argparse
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
    crd_timeout_sec: int = 900,
) -> None:
    d = _operator_config_dir()
    if not d.is_dir():
        raise OperatorConfigError(f"Directory not found: {d}")

    _apply_file(d / "monitoring-config-cm.yaml", kubeconfig=kubeconfig)
    _apply_file(d / "supported-nic-ids.yaml", kubeconfig=kubeconfig)

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
        description="Apply operator-config manifests after operators install CRDs.",
    )
    p.add_argument(
        "--kubeconfig",
        help="Path to kubeconfig (overrides KUBECONFIG / KUBECONFIG_PATH).",
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
        apply_operator_config(kubeconfig=kc, crd_timeout_sec=args.crd_timeout)
    except OperatorConfigError as e:
        print(f"Error: {e}", file=sys.stderr, flush=True)
        return 1
    except KeyboardInterrupt:
        print("Interrupted.", file=sys.stderr, flush=True)
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
