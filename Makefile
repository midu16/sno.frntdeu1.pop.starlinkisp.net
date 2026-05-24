SCRIPT    = idrac_sushy.py
TEST_FILE = test_idrac_sushy.py
VENV_DIR  = .venv
# Use venv Python when present (after make deps), else system python3
PYTHON    = $(or $(wildcard $(VENV_DIR)/bin/python3),python3)
PYTEST    = $(PYTHON) -m pytest

export IDRAC_PW
export IDRAC_IP
export IDRAC_USER
# Optional: openshift-install allows ~90m per wait-for install-complete; default 2 attempts in idrac_sushy.py
export INSTALL_WAIT_ATTEMPTS
# Optional: after primary waits fail, extra install-complete rounds if kubeconfig exists (see idrac_sushy.py)
export REMEDIATION_INSTALL_WAIT_ATTEMPTS

OCP_VERSION ?=
OCP_FLAG     = $(if $(OCP_VERSION),--ocp-version $(OCP_VERSION),)

.DEFAULT_GOAL := help

.PHONY: help deps install preflight ssh-key extract-installer prepare-configs \
        build-iso copy-iso deploy status eject restart power-on power-off \
        set-boot-cd set-boot-hdd wait-power-on wait-install \
        test test-verbose test-coverage lint clean

# ---- Help -------------------------------------------------------------------

help:
	@echo ""
	@echo "SNO OpenShift Installer — Makefile targets"
	@echo ""
	@echo "  Setup"
	@echo "    deps               Create .venv (if needed) and pip install -r requirements.txt"
	@echo "    clean              Remove workdir, caches, openshift-install"
	@echo ""
	@echo "  Full workflow"
	@echo "    install            Full end-to-end SNO installation"
	@echo ""
	@echo "  Individual steps"
	@echo "    preflight          Check/install prerequisites"
	@echo "    ssh-key            Generate SSH key and copy to webcache host"
	@echo "    extract-installer  Extract openshift-install from OCP release"
	@echo "    prepare-configs    Prepare workdir with templated configs"
	@echo "    build-iso          Build agent ISO"
	@echo "    copy-iso           SCP ISO to webcache host"
	@echo "    deploy             iDRAC: eject → insert → boot-cd → restart → wait"
	@echo "    wait-install       Wait for install-complete"
	@echo ""
	@echo "  iDRAC operations"
	@echo "    status             Show system model, power state, virtual media"
	@echo "    eject              Eject virtual media"
	@echo "    set-boot-cd        Set one-time boot to VirtualCD"
	@echo "    set-boot-hdd       Set one-time boot to HDD"
	@echo "    restart            Force-restart server"
	@echo "    power-on           Power on server"
	@echo "    power-off          Force power off server"
	@echo "    wait-power-on      Wait for power-on state"
	@echo ""
	@echo "  Testing"
	@echo "    test               Run functional tests"
	@echo "    test-verbose       Run tests with stdout capture disabled"
	@echo "    test-coverage      Run tests with coverage report"
	@echo "    lint               Run flake8 linter"
	@echo ""
	@echo "  Environment / Make variables"
	@echo "    IDRAC_PW           iDRAC password (required for iDRAC ops)"
	@echo "    IDRAC_IP           iDRAC IP (default: 192.168.1.228)"
	@echo "    IDRAC_USER         iDRAC username (default: root)"
	@echo "    INSTALL_WAIT_ATTEMPTS        Primary install-complete retries (default: 2)"
	@echo "    REMEDIATION_INSTALL_WAIT_ATTEMPTS  Extra rounds after primary failure if kubeconfig exists (default: 0)"
	@echo "    OCP_VERSION        OpenShift version (default: 4.22.0-ec.3)"
	@echo ""
	@echo "  Examples"
	@echo "    make install IDRAC_PW='pass' OCP_VERSION=4.18.6"
	@echo "    make extract-installer OCP_VERSION=4.17.0"
	@echo "    make status IDRAC_PW='pass'"
	@echo ""

# ---- Setup ------------------------------------------------------------------

# Create .venv if missing and install deps (avoids externally-managed-environment on Debian/Ubuntu)
deps:
	@if [ ! -d "$(VENV_DIR)" ]; then \
		echo "Creating virtual environment in $(VENV_DIR)..."; \
		if ! python3 -m venv $(VENV_DIR) 2>/dev/null; then \
			echo "python3-venv missing. Installing (OS detection)..."; \
			if command -v apt-get >/dev/null 2>&1; then sudo apt-get update -qq && sudo apt-get install -y python3-venv python3-full; \
			elif command -v dnf >/dev/null 2>&1; then sudo dnf install -y python3-virtualenv; \
			elif command -v yum >/dev/null 2>&1; then sudo yum install -y python3-virtualenv; \
			else echo "ERROR: Install python3-venv (Debian/Ubuntu) or python3-virtualenv (RHEL/Fedora) and re-run make deps"; exit 1; fi; \
			python3 -m venv $(VENV_DIR); \
		fi; \
	fi; \
	$(VENV_DIR)/bin/pip install --upgrade pip; \
	$(VENV_DIR)/bin/pip install -r requirements.txt
	@echo "Dependencies installed. Use: make install (or $(VENV_DIR)/bin/python3 $(SCRIPT) ...)"

clean:
	rm -rf workdir/ openshift-install __pycache__ .pytest_cache htmlcov .coverage
	find . -name '*.pyc' -delete 2>/dev/null || true

# ---- Full workflow -----------------------------------------------------------

install:
	$(PYTHON) $(SCRIPT) $(OCP_FLAG) install

# ---- Individual steps --------------------------------------------------------

preflight:
	$(PYTHON) $(SCRIPT) preflight

ssh-key:
	$(PYTHON) $(SCRIPT) ensure-ssh-key

extract-installer:
	$(PYTHON) $(SCRIPT) $(OCP_FLAG) extract-installer

prepare-configs:
	$(PYTHON) $(SCRIPT) prepare-configs

build-iso:
	$(PYTHON) $(SCRIPT) build-iso

copy-iso:
	$(PYTHON) $(SCRIPT) copy-iso

deploy:
	$(PYTHON) $(SCRIPT) deploy $(ISO_URL)

wait-install:
	$(PYTHON) $(SCRIPT) wait-install

# ---- iDRAC operations -------------------------------------------------------

status:
	$(PYTHON) $(SCRIPT) status

eject:
	$(PYTHON) $(SCRIPT) eject

set-boot-cd:
	$(PYTHON) $(SCRIPT) set-boot-cd

set-boot-hdd:
	$(PYTHON) $(SCRIPT) set-boot-hdd

restart:
	$(PYTHON) $(SCRIPT) restart

power-on:
	$(PYTHON) $(SCRIPT) power-on

power-off:
	$(PYTHON) $(SCRIPT) power-off

wait-power-on:
	$(PYTHON) $(SCRIPT) wait-power-on

# ---- Testing -----------------------------------------------------------------

test:
	$(PYTEST) $(TEST_FILE) -v

test-verbose:
	$(PYTEST) $(TEST_FILE) -v -s

test-coverage:
	$(PYTEST) $(TEST_FILE) -v --cov=idrac_sushy --cov-report=term-missing --cov-report=html

lint:
	$(PYTHON) -m flake8 $(SCRIPT) $(TEST_FILE) --max-line-length=120 --ignore=E501,W503
