#!/usr/bin/env python3
"""Functional tests for idrac_sushy.py — SNO OpenShift installer.

Run with:  pytest test_idrac_sushy.py -v
"""

import argparse
import json
import os
import subprocess
import sys
import textwrap
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock, call, patch

import pytest

sys.path.insert(0, os.path.dirname(__file__))
import idrac_sushy


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

@pytest.fixture
def default_args():
    """Minimal argparse-like namespace with all defaults populated."""
    return SimpleNamespace(
        ip="192.168.1.228",
        user="root",
        password="testpass",
        workdir="./workdir",
        src_dir="./abi-master-0",
        installer="./openshift-install",
        ocp_version="4.22.0-ec.3",
        remote_user="rock",
        remote_host="192.168.1.21",
        remote_path="/apps/webcache/OSs/",
        ssh_key=str(Path.home() / ".ssh" / "id_ed25519.pub"),
        registry_auth=str(Path.home() / ".docker" / "config.json"),
        iso_url="http://192.168.1.21:8080/OSs/agent.x86_64.iso",
        attempts=30,
        interval=10,
        command="status",
        install_wait_attempts=2,
    )


@pytest.fixture
def src_tree(tmp_path):
    """Create a realistic source config tree under tmp_path."""
    src = tmp_path / "abi-master-0"
    openshift_dir = src / "openshift"
    openshift_dir.mkdir(parents=True)
    (openshift_dir / "crun.yaml").write_text("kind: MachineConfig\n")
    (openshift_dir / "pao.yaml").write_text("kind: Subscription\n")

    (src / "agent-config.yaml").write_text(textwrap.dedent("""\
        apiVersion: v1alpha1
        metadata:
          name: sno
        rendezvousIP: 192.168.1.133
    """))

    (src / "install-config.yaml").write_text(textwrap.dedent("""\
        apiVersion: v1
        baseDomain: frntdeu1.pop.starlinkisp.net
        metadata:
          name: sno
        pullSecret: '{"auths":{<pull_secret>}}'
        sshKey: 'ssh-ed25519 <ssh_key> <user>@<host>'
    """))

    return src


@pytest.fixture
def registry_auth_file(tmp_path):
    """Create a fake Docker config.json."""
    auth_file = tmp_path / "config.json"
    auth_file.write_text(json.dumps({
        "auths": {"quay.io": {"auth": "dGVzdDp0ZXN0"}}
    }))
    return auth_file


@pytest.fixture
def ssh_key_file(tmp_path):
    """Create a fake SSH public key file."""
    key_file = tmp_path / "id_ed25519.pub"
    key_file.write_text("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey user@host\n")
    return key_file


@pytest.fixture
def mock_sushy():
    """Create a mock sushy module with all needed constants and types."""
    mock = MagicMock()
    mock.VIRTUAL_MEDIA_CD = "CD"
    mock.VIRTUAL_MEDIA_HDD = "HDD"
    mock.SYSTEM_POWER_STATE_ON = "On"
    mock.RESET_TYPE_FORCE_RESTART = "ForceRestart"
    mock.RESET_TYPE_ON = "On"
    mock.RESET_TYPE_FORCE_OFF = "ForceOff"
    mock.exceptions = MagicMock()
    mock.exceptions.ServerSideError = type("ServerSideError", (Exception,), {})
    return mock


@pytest.fixture
def mock_idrac(mock_sushy):
    """Full iDRAC mock: root, manager, system, cd_device, oem."""
    system = MagicMock()
    system.model = "PowerEdge R640"
    system.power_state = mock_sushy.SYSTEM_POWER_STATE_ON

    cd_device = MagicMock()
    cd_device.media_types = [mock_sushy.VIRTUAL_MEDIA_CD]
    cd_device.inserted = False
    cd_device.image = None

    oem = MagicMock()

    manager = MagicMock()
    manager.virtual_media.get_members.return_value = [cd_device]
    manager.get_oem_extension.return_value = oem

    manager_collection = MagicMock()
    manager_collection.get_members.return_value = [manager]
    system_collection = MagicMock()
    system_collection.get_members.return_value = [system]

    root = MagicMock()
    root.get_manager_collection.return_value = manager_collection
    root.get_system_collection.return_value = system_collection

    mock_sushy.Sushy.return_value = root

    return {
        "sushy": mock_sushy,
        "root": root,
        "manager": manager,
        "system": system,
        "cd_device": cd_device,
        "oem": oem,
    }


# ---------------------------------------------------------------------------
# Password management
# ---------------------------------------------------------------------------

class TestDecryptPassword:
    def test_file_not_found(self):
        with pytest.raises(idrac_sushy.InstallerError, match="not found"):
            idrac_sushy.decrypt_password("/nonexistent/idrac_pw.enc", "pass")

    def test_decrypt_success(self, tmp_path):
        plain = tmp_path / "plain.txt"
        plain.write_text("supersecret")
        enc = tmp_path / "idrac_pw.enc"
        subprocess.run(
            ["openssl", "enc", "-aes-256-cbc", "-salt", "-pbkdf2",
             "-in", str(plain), "-out", str(enc), "-pass", "pass:mypass"],
            check=True, capture_output=True,
        )
        result = idrac_sushy.decrypt_password(str(enc), passphrase="mypass")
        assert result == "supersecret"

    def test_decrypt_wrong_passphrase(self, tmp_path):
        plain = tmp_path / "plain.txt"
        plain.write_text("supersecret")
        enc = tmp_path / "idrac_pw.enc"
        subprocess.run(
            ["openssl", "enc", "-aes-256-cbc", "-salt", "-pbkdf2",
             "-in", str(plain), "-out", str(enc), "-pass", "pass:mypass"],
            check=True, capture_output=True,
        )
        with pytest.raises(idrac_sushy.InstallerError, match="Decrypt failed"):
            idrac_sushy.decrypt_password(str(enc), passphrase="wrongpass")


class TestResolvePassword:
    def test_from_args(self, default_args):
        default_args.password = "fromargs"
        assert idrac_sushy.resolve_password(default_args) == "fromargs"

    def test_from_env(self, default_args, monkeypatch):
        default_args.password = None
        monkeypatch.setenv("IDRAC_PW", "fromenv")
        assert idrac_sushy.resolve_password(default_args) == "fromenv"

    def test_from_encrypted_file(self, default_args, tmp_path, monkeypatch):
        default_args.password = None
        monkeypatch.delenv("IDRAC_PW", raising=False)
        plain = tmp_path / "plain.txt"
        plain.write_text("fromfile")
        enc = tmp_path / "idrac_pw.enc"
        subprocess.run(
            ["openssl", "enc", "-aes-256-cbc", "-salt", "-pbkdf2",
             "-in", str(plain), "-out", str(enc), "-pass", "pass:test"],
            check=True, capture_output=True,
        )
        monkeypatch.chdir(tmp_path)
        with patch.object(idrac_sushy, "decrypt_password",
                          wraps=lambda f, passphrase=None: "fromfile"):
            result = idrac_sushy.resolve_password(default_args)
        assert result == "fromfile"

    def test_no_password_available(self, default_args, tmp_path, monkeypatch):
        default_args.password = None
        monkeypatch.delenv("IDRAC_PW", raising=False)
        monkeypatch.chdir(tmp_path)
        with pytest.raises(idrac_sushy.InstallerError, match="No iDRAC password"):
            idrac_sushy.resolve_password(default_args)


# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------

class TestCheckCommand:
    def test_existing_command(self):
        assert idrac_sushy.check_command("python3") is True

    def test_missing_command(self):
        assert idrac_sushy.check_command("nonexistent_tool_xyz") is False


class TestPreflight:
    @patch.object(idrac_sushy, "ensure_nmstatectl", return_value=True)
    @patch.object(idrac_sushy, "ensure_sushy", return_value=True)
    @patch.object(idrac_sushy, "check_command", return_value=True)
    def test_all_ok(self, mock_check, mock_sushy, mock_nm, default_args):
        idrac_sushy.cmd_preflight(default_args)

    @patch.object(idrac_sushy, "ensure_nmstatectl", return_value=True)
    @patch.object(idrac_sushy, "ensure_sushy", return_value=True)
    @patch.object(idrac_sushy, "check_command", return_value=False)
    def test_missing_tool_fails(self, mock_check, mock_sushy, mock_nm, default_args):
        with pytest.raises(idrac_sushy.InstallerError, match="prerequisites"):
            idrac_sushy.cmd_preflight(default_args)


# ---------------------------------------------------------------------------
# SSH key
# ---------------------------------------------------------------------------

class TestEnsureSSHKey:
    @patch.object(idrac_sushy, "run_cmd")
    def test_key_exists_copies(self, mock_run, default_args, ssh_key_file):
        default_args.ssh_key = str(ssh_key_file)
        idrac_sushy.cmd_ensure_ssh_key(default_args)
        assert mock_run.call_count == 1
        sshpass_call = mock_run.call_args[0][0]
        assert "sshpass" in sshpass_call
        assert "ssh-copy-id" in sshpass_call

    @patch.object(idrac_sushy, "run_cmd")
    def test_key_missing_generates_and_copies(self, mock_run, default_args, tmp_path):
        key_path = tmp_path / "new_key.pub"
        default_args.ssh_key = str(key_path)
        idrac_sushy.cmd_ensure_ssh_key(default_args)
        assert mock_run.call_count == 2
        keygen_call = mock_run.call_args_list[0][0][0]
        assert "ssh-keygen" in keygen_call


# ---------------------------------------------------------------------------
# Config templating
# ---------------------------------------------------------------------------

class TestTemplateInstallConfig:
    def test_replaces_placeholders(self, tmp_path, src_tree, registry_auth_file, ssh_key_file):
        dst = tmp_path / "install-config.yaml"
        idrac_sushy.template_install_config(
            src_tree / "install-config.yaml",
            dst,
            registry_auth_file,
            ssh_key_file,
        )
        content = dst.read_text()
        assert "<pull_secret>" not in content
        assert "<ssh_key>" not in content
        assert "quay.io" in content
        assert "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5" in content

    def test_preserves_other_fields(self, tmp_path, src_tree, registry_auth_file, ssh_key_file):
        dst = tmp_path / "install-config.yaml"
        idrac_sushy.template_install_config(
            src_tree / "install-config.yaml",
            dst,
            registry_auth_file,
            ssh_key_file,
        )
        content = dst.read_text()
        assert "baseDomain: frntdeu1.pop.starlinkisp.net" in content
        assert "name: sno" in content


# ---------------------------------------------------------------------------
# Prepare configs
# ---------------------------------------------------------------------------

class TestPrepareConfigs:
    def test_creates_workdir_and_copies(self, tmp_path, src_tree, registry_auth_file, ssh_key_file):
        workdir = tmp_path / "workdir"
        args = SimpleNamespace(
            workdir=str(workdir),
            src_dir=str(src_tree),
            registry_auth=str(registry_auth_file),
            ssh_key=str(ssh_key_file),
        )
        idrac_sushy.cmd_prepare_configs(args)

        assert (workdir / "openshift" / "crun.yaml").exists()
        assert (workdir / "openshift" / "pao.yaml").exists()
        assert (workdir / "agent-config.yaml").exists()
        ic = (workdir / "install-config.yaml").read_text()
        assert "quay.io" in ic

    def test_cleans_existing_workdir(self, tmp_path, src_tree, registry_auth_file, ssh_key_file):
        workdir = tmp_path / "workdir"
        workdir.mkdir()
        (workdir / "stale-file.txt").write_text("old data")

        args = SimpleNamespace(
            workdir=str(workdir),
            src_dir=str(src_tree),
            registry_auth=str(registry_auth_file),
            ssh_key=str(ssh_key_file),
        )
        idrac_sushy.cmd_prepare_configs(args)
        assert not (workdir / "stale-file.txt").exists()
        assert (workdir / "agent-config.yaml").exists()

    def test_missing_source_dir_fails(self, tmp_path, registry_auth_file, ssh_key_file):
        args = SimpleNamespace(
            workdir=str(tmp_path / "workdir"),
            src_dir=str(tmp_path / "nonexistent"),
            registry_auth=str(registry_auth_file),
            ssh_key=str(ssh_key_file),
        )
        with pytest.raises(idrac_sushy.InstallerError, match="not found"):
            idrac_sushy.cmd_prepare_configs(args)

    def test_missing_registry_auth_fails(self, tmp_path, src_tree, ssh_key_file):
        args = SimpleNamespace(
            workdir=str(tmp_path / "workdir"),
            src_dir=str(src_tree),
            registry_auth=str(tmp_path / "no_config.json"),
            ssh_key=str(ssh_key_file),
        )
        with pytest.raises(idrac_sushy.InstallerError, match="Registry auth not found"):
            idrac_sushy.cmd_prepare_configs(args)


# ---------------------------------------------------------------------------
# Extract installer
# ---------------------------------------------------------------------------

class TestExtractInstaller:
    @patch.object(idrac_sushy, "run_cmd")
    def test_extracts_installer(self, mock_run, default_args, registry_auth_file):
        default_args.registry_auth = str(registry_auth_file)
        mock_run.return_value = MagicMock(
            stdout="Pull From: quay.io/openshift-release-dev/ocp-release@sha256:abc123\n",
            returncode=0,
        )
        idrac_sushy.cmd_extract_installer(default_args)
        assert mock_run.call_count == 2
        first_call = mock_run.call_args_list[0][0][0]
        assert "oc" in first_call
        assert "release" in first_call
        assert "info" in first_call

    @patch.object(idrac_sushy, "run_cmd")
    def test_no_digest_fails(self, mock_run, default_args, registry_auth_file):
        default_args.registry_auth = str(registry_auth_file)
        mock_run.return_value = MagicMock(stdout="some unrelated output\n", returncode=0)
        with pytest.raises(idrac_sushy.InstallerError, match="digest"):
            idrac_sushy.cmd_extract_installer(default_args)

    def test_missing_auth_file_fails(self, default_args, tmp_path):
        default_args.registry_auth = str(tmp_path / "nonexistent.json")
        with pytest.raises(idrac_sushy.InstallerError, match="Registry auth not found"):
            idrac_sushy.cmd_extract_installer(default_args)


# ---------------------------------------------------------------------------
# Build ISO
# ---------------------------------------------------------------------------

class TestBuildISO:
    @patch.object(idrac_sushy, "run_cmd")
    def test_build_success(self, mock_run, default_args, tmp_path):
        workdir = tmp_path / "workdir"
        workdir.mkdir()
        iso = workdir / "agent.x86_64.iso"
        iso.write_bytes(b"\x00" * 1024)
        default_args.workdir = str(workdir)
        installer = tmp_path / "openshift-install"
        installer.touch()
        default_args.installer = str(installer)

        idrac_sushy.cmd_build_iso(default_args)
        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert "agent" in cmd
        assert "create" in cmd
        assert "image" in cmd

    def test_missing_installer_fails(self, default_args, tmp_path):
        default_args.installer = str(tmp_path / "nonexistent")
        with pytest.raises(idrac_sushy.InstallerError, match="Installer not found"):
            idrac_sushy.cmd_build_iso(default_args)

    @patch.object(idrac_sushy, "run_cmd")
    def test_missing_iso_after_build_fails(self, mock_run, default_args, tmp_path):
        workdir = tmp_path / "workdir"
        workdir.mkdir()
        default_args.workdir = str(workdir)
        installer = tmp_path / "openshift-install"
        installer.touch()
        default_args.installer = str(installer)

        with pytest.raises(idrac_sushy.InstallerError, match="ISO not found"):
            idrac_sushy.cmd_build_iso(default_args)


# ---------------------------------------------------------------------------
# Copy ISO
# ---------------------------------------------------------------------------

class TestCopyISO:
    @patch.object(idrac_sushy, "run_cmd")
    def test_copy_success(self, mock_run, default_args, tmp_path):
        workdir = tmp_path / "workdir"
        workdir.mkdir()
        (workdir / "agent.x86_64.iso").write_bytes(b"\x00" * 512)
        default_args.workdir = str(workdir)

        idrac_sushy.cmd_copy_iso(default_args)
        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert cmd[0] == "scp"
        assert "rock@192.168.1.21:/apps/webcache/OSs/" in cmd[-1]

    def test_missing_iso_fails(self, default_args, tmp_path):
        default_args.workdir = str(tmp_path / "empty")
        with pytest.raises(idrac_sushy.InstallerError, match="ISO not found"):
            idrac_sushy.cmd_copy_iso(default_args)


# ---------------------------------------------------------------------------
# Wait install
# ---------------------------------------------------------------------------

class TestWaitInstall:
    @patch.object(idrac_sushy, "run_cmd")
    def test_wait_calls_installer(self, mock_run, default_args, tmp_path):
        installer = tmp_path / "openshift-install"
        installer.touch()
        default_args.installer = str(installer)
        default_args.workdir = str(tmp_path / "workdir")

        idrac_sushy.cmd_wait_install(default_args)
        cmd = mock_run.call_args[0][0]
        assert "wait-for" in cmd
        assert "install-complete" in cmd
        assert mock_run.call_count == 1

    @patch.object(idrac_sushy, "run_cmd")
    def test_wait_retries_on_failure(self, mock_run, default_args, tmp_path):
        installer = tmp_path / "openshift-install"
        installer.touch()
        default_args.installer = str(installer)
        default_args.workdir = str(tmp_path / "workdir")
        default_args.install_wait_attempts = 2
        mock_run.side_effect = [
            subprocess.CalledProcessError(6, [str(installer)]),
            None,
        ]

        idrac_sushy.cmd_wait_install(default_args)
        assert mock_run.call_count == 2

    @patch.object(idrac_sushy, "run_cmd")
    def test_wait_raises_after_exhausting_attempts(self, mock_run, default_args, tmp_path):
        installer = tmp_path / "openshift-install"
        installer.touch()
        default_args.installer = str(installer)
        default_args.workdir = str(tmp_path / "workdir")
        default_args.install_wait_attempts = 2
        err = subprocess.CalledProcessError(6, [str(installer)])
        mock_run.side_effect = [err, err]

        with pytest.raises(subprocess.CalledProcessError):
            idrac_sushy.cmd_wait_install(default_args)
        assert mock_run.call_count == 2

    def test_missing_installer_fails(self, default_args, tmp_path):
        default_args.installer = str(tmp_path / "nonexistent")
        with pytest.raises(idrac_sushy.InstallerError, match="Installer not found"):
            idrac_sushy.cmd_wait_install(default_args)


# ---------------------------------------------------------------------------
# iDRAC operations (sushy)
# ---------------------------------------------------------------------------

class TestIDRACConnect:
    def test_connect_returns_objects(self, mock_idrac):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            root, manager, system = idrac_sushy.connect("1.2.3.4", "root", "pass")
        mock_idrac["sushy"].Sushy.assert_called_once_with(
            "https://1.2.3.4", username="root", password="pass", verify=False,
        )
        assert root == mock_idrac["root"]
        assert manager == mock_idrac["manager"]
        assert system == mock_idrac["system"]


class TestFindCDDevice:
    def test_finds_cd(self, mock_idrac):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            cd = idrac_sushy.find_cd_device(mock_idrac["manager"])
        assert cd == mock_idrac["cd_device"]

    def test_returns_none_when_no_cd(self, mock_idrac):
        mock_idrac["cd_device"].media_types = ["Floppy"]
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            cd = idrac_sushy.find_cd_device(mock_idrac["manager"])
        assert cd is None


class TestRequireCD:
    def test_raises_when_no_cd(self, mock_idrac):
        mock_idrac["manager"].virtual_media.get_members.return_value = []
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with pytest.raises(idrac_sushy.InstallerError, match="VirtualCD"):
                idrac_sushy.require_cd(mock_idrac["manager"])


class TestIDRACStatus:
    def test_prints_status(self, mock_idrac, default_args, capsys):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_status(default_args)
        out = capsys.readouterr().out
        assert "PowerEdge R640" in out
        assert "On" in out


class TestIDRACEject:
    def test_eject_success(self, mock_idrac, default_args):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_eject(default_args)
        mock_idrac["cd_device"].eject_media.assert_called_once()

    def test_eject_nothing_mounted(self, mock_idrac, default_args, capsys):
        ServerSideError = mock_idrac["sushy"].exceptions.ServerSideError
        mock_idrac["cd_device"].eject_media.side_effect = ServerSideError("no media")
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_eject(default_args)
        out = capsys.readouterr().out
        assert "nothing to eject" in out.lower()


class TestIDRACInsert:
    @patch("time.sleep")
    def test_insert_success(self, mock_sleep, mock_idrac, default_args):
        default_args.iso_url = "http://192.168.1.21:8080/OSs/agent.x86_64.iso"
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_insert(default_args)
        mock_idrac["cd_device"].insert_media.assert_called_once_with(default_args.iso_url)

    def test_insert_redfish_error(self, mock_idrac, default_args):
        ServerSideError = mock_idrac["sushy"].exceptions.ServerSideError
        mock_idrac["cd_device"].insert_media.side_effect = ServerSideError("Connection refused")
        default_args.iso_url = "http://192.168.1.21:8080/OSs/agent.x86_64.iso"
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                with pytest.raises(idrac_sushy.InstallerError, match="Virtual media insert failed"):
                    idrac_sushy.cmd_insert(default_args)


class TestIDRACSetBootCD:
    def test_set_boot_cd(self, mock_idrac, default_args):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_set_boot_cd(default_args)
        mock_idrac["oem"].set_virtual_boot_device.assert_called_once_with(
            "CD", persistent=False, manager=mock_idrac["manager"],
        )


class TestIDRACSetBootHDD:
    def test_set_boot_hdd(self, mock_idrac, default_args):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_set_boot_hdd(default_args)
        mock_idrac["oem"].set_virtual_boot_device.assert_called_once_with(
            "HDD", persistent=False, manager=mock_idrac["manager"],
        )


class TestIDRACRestart:
    def test_restart(self, mock_idrac, default_args):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_restart(default_args)
        mock_idrac["system"].reset_system.assert_called_once_with("ForceRestart")


class TestIDRACPowerOn:
    def test_power_on(self, mock_idrac, default_args):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_power_on(default_args)
        mock_idrac["system"].reset_system.assert_called_once_with("On")


class TestIDRACPowerOff:
    def test_power_off(self, mock_idrac, default_args):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_power_off(default_args)
        mock_idrac["system"].reset_system.assert_called_once_with("ForceOff")


class TestIDRACWaitPowerOn:
    @patch("time.sleep")
    def test_immediate_power_on(self, mock_sleep, mock_idrac, default_args):
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_wait_power_on(default_args)

    @patch("time.sleep")
    def test_timeout_raises(self, mock_sleep, mock_idrac, default_args):
        mock_idrac["system"].power_state = "Off"
        default_args.attempts = 2
        default_args.interval = 0
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                with pytest.raises(idrac_sushy.InstallerError, match="Timeout"):
                    idrac_sushy.cmd_wait_power_on(default_args)


class TestIDRACDeploy:
    @patch("time.sleep")
    def test_full_deploy_cycle(self, mock_sleep, mock_idrac, default_args):
        default_args.iso_url = "http://192.168.1.21:8080/OSs/agent.x86_64.iso"
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                idrac_sushy.cmd_deploy(default_args)

        mock_idrac["cd_device"].eject_media.assert_called_once()
        mock_idrac["cd_device"].insert_media.assert_called_once_with(default_args.iso_url)
        mock_idrac["oem"].set_virtual_boot_device.assert_called_once()
        mock_idrac["system"].reset_system.assert_called_once_with("ForceRestart")

    @patch("time.sleep")
    def test_deploy_timeout_raises(self, mock_sleep, mock_idrac, default_args):
        default_args.iso_url = "http://192.168.1.21:8080/OSs/agent.x86_64.iso"
        mock_idrac["system"].power_state = "Off"
        with patch.object(idrac_sushy, "_get_sushy", return_value=mock_idrac["sushy"]):
            with patch.object(idrac_sushy, "connect",
                              return_value=(mock_idrac["root"], mock_idrac["manager"], mock_idrac["system"])):
                with pytest.raises(idrac_sushy.InstallerError, match="Timeout"):
                    idrac_sushy.cmd_deploy(default_args)


# ---------------------------------------------------------------------------
# Full install orchestration
# ---------------------------------------------------------------------------

class TestInstall:
    def test_install_calls_all_steps_in_order(self, default_args):
        called = []

        def make_spy(name):
            def spy(args):
                called.append(name)
            return spy

        with patch.object(idrac_sushy, "cmd_preflight", make_spy("preflight")), \
             patch.object(idrac_sushy, "cmd_ensure_ssh_key", make_spy("ssh")), \
             patch.object(idrac_sushy, "cmd_extract_installer", make_spy("extract")), \
             patch.object(idrac_sushy, "cmd_prepare_configs", make_spy("configs")), \
             patch.object(idrac_sushy, "cmd_build_iso", make_spy("build")), \
             patch.object(idrac_sushy, "cmd_copy_iso", make_spy("copy")), \
             patch.object(idrac_sushy, "cmd_deploy", make_spy("deploy")), \
             patch.object(idrac_sushy, "cmd_wait_install", make_spy("wait")):
            idrac_sushy.cmd_install(default_args)

        assert called == ["preflight", "ssh", "extract", "configs", "build", "copy", "deploy", "wait"]

    def test_install_sets_iso_url_from_remote_host(self, default_args):
        default_args.iso_url = None
        default_args.remote_host = "10.0.0.1"
        captured_args = {}

        def spy_deploy(args):
            captured_args["iso_url"] = args.iso_url

        with patch.object(idrac_sushy, "cmd_preflight", lambda a: None), \
             patch.object(idrac_sushy, "cmd_ensure_ssh_key", lambda a: None), \
             patch.object(idrac_sushy, "cmd_extract_installer", lambda a: None), \
             patch.object(idrac_sushy, "cmd_prepare_configs", lambda a: None), \
             patch.object(idrac_sushy, "cmd_build_iso", lambda a: None), \
             patch.object(idrac_sushy, "cmd_copy_iso", lambda a: None), \
             patch.object(idrac_sushy, "cmd_deploy", spy_deploy), \
             patch.object(idrac_sushy, "cmd_wait_install", lambda a: None):
            idrac_sushy.cmd_install(default_args)

        assert captured_args["iso_url"] == "http://10.0.0.1:8080/OSs/agent.x86_64.iso"

    def test_install_stops_on_step_failure(self, default_args):
        def fail_extract(args):
            raise idrac_sushy.InstallerError("extract failed")

        with patch.object(idrac_sushy, "cmd_preflight", lambda a: None), \
             patch.object(idrac_sushy, "cmd_ensure_ssh_key", lambda a: None), \
             patch.object(idrac_sushy, "cmd_extract_installer", fail_extract):
            with pytest.raises(idrac_sushy.InstallerError, match="extract failed"):
                idrac_sushy.cmd_install(default_args)


# ---------------------------------------------------------------------------
# CLI argument parsing
# ---------------------------------------------------------------------------

class TestCLI:
    def _parse(self, argv):
        parser = idrac_sushy.build_parser()
        return parser.parse_args(argv)

    def test_parse_status(self):
        args = self._parse(["status"])
        assert args.command == "status"

    def test_parse_eject(self):
        args = self._parse(["eject"])
        assert args.command == "eject"

    def test_parse_insert(self):
        args = self._parse(["insert", "http://example.com/test.iso"])
        assert args.command == "insert"
        assert args.iso_url == "http://example.com/test.iso"

    def test_parse_set_boot_cd(self):
        args = self._parse(["set-boot-cd"])
        assert args.command == "set-boot-cd"

    def test_parse_set_boot_hdd(self):
        args = self._parse(["set-boot-hdd"])
        assert args.command == "set-boot-hdd"

    def test_parse_restart(self):
        args = self._parse(["restart"])
        assert args.command == "restart"

    def test_parse_power_on(self):
        args = self._parse(["power-on"])
        assert args.command == "power-on"

    def test_parse_power_off(self):
        args = self._parse(["power-off"])
        assert args.command == "power-off"

    def test_parse_wait_power_on_defaults(self):
        args = self._parse(["wait-power-on"])
        assert args.command == "wait-power-on"
        assert args.attempts == 30
        assert args.interval == 10

    def test_parse_wait_power_on_custom(self):
        args = self._parse(["wait-power-on", "--attempts", "5", "--interval", "2"])
        assert args.attempts == 5
        assert args.interval == 2

    def test_parse_deploy(self):
        args = self._parse(["deploy", "http://example.com/agent.iso"])
        assert args.command == "deploy"
        assert args.iso_url == "http://example.com/agent.iso"

    def test_parse_install(self):
        args = self._parse(["install"])
        assert args.command == "install"

    def test_parse_install_with_iso_url(self):
        args = self._parse(["install", "--iso-url", "http://custom/agent.iso"])
        assert args.iso_url == "http://custom/agent.iso"

    def test_parse_preflight(self):
        args = self._parse(["preflight"])
        assert args.command == "preflight"

    def test_parse_ensure_ssh_key(self):
        args = self._parse(["ensure-ssh-key"])
        assert args.command == "ensure-ssh-key"

    def test_parse_extract_installer(self):
        args = self._parse(["extract-installer"])
        assert args.command == "extract-installer"

    def test_parse_prepare_configs(self):
        args = self._parse(["prepare-configs"])
        assert args.command == "prepare-configs"

    def test_parse_build_iso(self):
        args = self._parse(["build-iso"])
        assert args.command == "build-iso"

    def test_parse_copy_iso(self):
        args = self._parse(["copy-iso"])
        assert args.command == "copy-iso"

    def test_parse_wait_install(self):
        args = self._parse(["wait-install"])
        assert args.command == "wait-install"

    def test_parse_common_options(self):
        args = self._parse([
            "--ip", "10.0.0.1",
            "--user", "admin",
            "--password", "secret",
            "--workdir", "/tmp/work",
            "--ocp-version", "4.17.0",
            "status",
        ])
        assert args.ip == "10.0.0.1"
        assert args.user == "admin"
        assert args.password == "secret"
        assert args.workdir == "/tmp/work"
        assert args.ocp_version == "4.17.0"

    def test_parse_no_command_fails(self):
        with pytest.raises(SystemExit):
            self._parse([])

    def test_dispatch_table_complete(self):
        parser = idrac_sushy.build_parser()
        subparsers_actions = [
            action for action in parser._subparsers._actions
            if isinstance(action, argparse._SubParsersAction)
        ]
        registered_commands = set(subparsers_actions[0].choices.keys())
        dispatch_commands = set(idrac_sushy.DISPATCH.keys())
        assert registered_commands == dispatch_commands, (
            f"Mismatch: parser={registered_commands - dispatch_commands}, "
            f"dispatch={dispatch_commands - registered_commands}"
        )


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

class TestHelpers:
    def test_attr_returns_args_value(self, default_args):
        assert idrac_sushy._attr(default_args, "ip") == "192.168.1.228"

    def test_attr_falls_back_to_defaults(self):
        args = SimpleNamespace()
        assert idrac_sushy._attr(args, "remote_user") == "rock"

    def test_attr_returns_none_for_unknown(self):
        args = SimpleNamespace()
        assert idrac_sushy._attr(args, "nonexistent_key") is None

    @patch("subprocess.run")
    def test_run_cmd_string(self, mock_run):
        idrac_sushy.run_cmd("echo hello")
        mock_run.assert_called_once()
        assert mock_run.call_args[0][0] == ["echo", "hello"]

    @patch("subprocess.run")
    def test_run_cmd_list(self, mock_run):
        idrac_sushy.run_cmd(["ls", "-la"])
        mock_run.assert_called_once()
        assert mock_run.call_args[0][0] == ["ls", "-la"]

    @patch("subprocess.run")
    def test_run_cmd_capture(self, mock_run):
        idrac_sushy.run_cmd(["echo", "hi"], capture=True)
        kwargs = mock_run.call_args[1]
        assert kwargs["capture_output"] is True
        assert kwargs["text"] is True


class TestInstallerError:
    def test_is_exception(self):
        assert issubclass(idrac_sushy.InstallerError, Exception)

    def test_message(self):
        err = idrac_sushy.InstallerError("test message")
        assert str(err) == "test message"


# ---------------------------------------------------------------------------
# Main entry point
# ---------------------------------------------------------------------------

class TestMain:
    def test_installer_error_exits_1(self):
        with patch.object(idrac_sushy, "build_parser") as mock_parser:
            mock_parser.return_value.parse_args.return_value = SimpleNamespace(command="preflight")
            with patch.dict(idrac_sushy.DISPATCH, {
                "preflight": MagicMock(side_effect=idrac_sushy.InstallerError("boom")),
            }):
                with pytest.raises(SystemExit) as exc_info:
                    idrac_sushy.main()
                assert exc_info.value.code == 1

    def test_success_exits_0(self):
        with patch.object(idrac_sushy, "build_parser") as mock_parser:
            mock_parser.return_value.parse_args.return_value = SimpleNamespace(command="preflight")
            with patch.dict(idrac_sushy.DISPATCH, {"preflight": MagicMock()}):
                idrac_sushy.main()
