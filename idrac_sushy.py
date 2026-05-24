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
  install            Full end-to-end SNO installation (all steps above)
"""

import argparse
import getpass
import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path
from subprocess import CalledProcessError

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
    "ssh_key": str(Path.home() / ".ssh" / "id_ed25519.pub"),
    "registry_auth": str(Path.home() / ".docker" / "config.json"),
}


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
    time.sleep(15)

    # 2 — Insert
    print(f"Inserting virtual media: {iso_url}")
    insert_virtual_media(cd, iso_url)
    time.sleep(10)
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

    # 4 — Force restart
    print("Restarting server (ForceRestart) ...")
    system.reset_system(sushy.RESET_TYPE_FORCE_RESTART)
    print("  Restart command sent.")
    time.sleep(30)

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
        print(f"Waiting for install-complete (attempt {attempt}/{attempts}) ...")
        try:
            run_cmd(cmd)
            print("Installation complete!")
            return
        except CalledProcessError as e:
            if attempt >= attempts:
                raise
            print(
                f"Install wait exited {e.returncode} (openshift-install allows ~90m per attempt). "
                "Cluster may still be reconciling MachineConfig; retrying ...",
                flush=True,
            )


def cmd_wait_install_maybe_remediate(args):
    """Run install-complete waits; if they fail but kubeconfig exists, run extra wait rounds.

    Helps when bootstrap finished and the API is up but MachineConfig (or other COs) needs
    longer than openshift-install's single deadline per attempt.
    """
    kubeconfig = Path(_attr(args, "workdir")).resolve() / "auth" / "kubeconfig"
    try:
        cmd_wait_install(args)
    except CalledProcessError:
        remediation = _remediation_install_attempts(args)
        if remediation < 1 or not kubeconfig.is_file():
            raise
        print(SEPARATOR)
        print(
            "[Remediation] install-complete waits failed while a kubeconfig exists; "
            "the cluster may still become ready (e.g. slow MachineConfig reconcile)."
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

    return parser


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
