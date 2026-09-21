# SNO OpenShift Installer — Go-native automation
#
# The install pipeline, iDRAC (Redfish) control, day-2 operator work and the
# MCP server are all implemented in Go under cmd/ and internal/. This Makefile
# builds the binaries and drives the CLI; it no longer depends on the legacy
# Python tooling (idrac_sushy.py / pytest / flake8).

BINARY       = sno-installer
MCP_BINARY   = sno-mcp
GO           ?= go
STATE        ?= config/sno-state.yaml
# Version stamped into the binaries at build time.
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS      = -s -w -X main.version=$(VERSION)

# Release selection. OCP_VERSION (X.Y.Z[-ec/fc/rc.N]) maps to the public quay
# tag; RELEASE_IMAGE (full pullspec) wins and is used verbatim for CI
# nightlies / mirrored images that do not follow that pattern.
OCP_VERSION   ?=
OCP_FLAG      = $(if $(OCP_VERSION),--ocp-version $(OCP_VERSION),)
RELEASE_IMAGE ?=
RELEASE_FLAG  = $(if $(RELEASE_IMAGE),--release-image $(RELEASE_IMAGE),)

# iDRAC access: IDRAC_PW is required for iDRAC operations and is NEVER
# committed to the repo (it is supplied via the environment / a CI secret).
export IDRAC_PW
export IDRAC_IP
export IDRAC_USER

# Optional pacing / retry knobs consumed by the Go installer (see
# internal/sno). Kept as plain exports so the `sno-installer` process sees them.
export INSTALL_WAIT_ATTEMPTS
export REMEDIATION_INSTALL_WAIT_ATTEMPTS
export API_READY_WAIT_SEC
export API_READY_POLL_SEC
export API_READY_SETTLE_SEC
export API_READY_STABLE_POLLS
export SKIP_MC_REMEDIATION
export MC_REMEDIATION_WAIT_SEC
export FIX_WORKLOAD_PINNING
export POST_COPY_ISO_SLEEP_SEC
export ISO_HTTP_PROBE
export IDRAC_DEPLOY_AFTER_EJECT_SEC
export IDRAC_DEPLOY_AFTER_INSERT_SEC
export IDRAC_DEPLOY_BEFORE_RESTART_SEC
export IDRAC_DEPLOY_AFTER_RESTART_SEC

.DEFAULT_GOAL := help

.PHONY: help build build-mcp install preflight ssh-key extract-installer \
        prepare-configs build-iso copy-iso deploy insert wait-install \
        wait-install-remediate wait-api-ready remediate-mco \
        status eject set-boot-cd set-boot-hdd restart power-on power-off \
        wait-power-on day2 operator-config node-exporter diagnostics sol \
        state-validate state-plan test test-verbose test-coverage \
        fmt vet lint clean tidy

# ---- Help --------------------------------------------------------------------

help:
	@echo ""
	@echo "SNO OpenShift Installer (Go-native) — Makefile targets"
	@echo ""
	@echo "  Build"
	@echo "    build              Build the $(BINARY) and $(MCP_BINARY) binaries"
	@echo "    tidy               go mod tidy"
	@echo "    clean              Remove binaries, workdir, openshift-install, caches"
	@echo ""
	@echo "  Full workflow"
	@echo "    install            Full end-to-end SNO installation (idempotent)"
	@echo ""
	@echo "  Individual steps"
	@echo "    preflight          Check prerequisites"
	@echo "    ssh-key            Generate SSH key and install on webcache host"
	@echo "    extract-installer  Extract openshift-install from the OCP release"
	@echo "    prepare-configs    Stage workdir with templated configs"
	@echo "    build-iso          Build the agent ISO"
	@echo "    copy-iso           SFTP the ISO to the webcache host (+ HTTP probe)"
	@echo "    insert <iso-url>   Mount virtual media from URL"
	@echo "    deploy <iso-url>   iDRAC: eject -> insert -> boot-cd -> restart -> wait"
	@echo "    wait-install       Wait for install-complete (API-ready gates)"
	@echo "    wait-install-remediate  wait-install + MCO remediation + extra rounds"
	@echo "    wait-api-ready     Block until kube-apiserver /readyz is stable"
	@echo "    remediate-mco      Stuck-MachineConfig recovery procedure"
	@echo "    day2               Post-install operator phases / config (see help)"
	@echo "    operator-config    Apply operator-config (OLM waits, CRs)"
	@echo "    node-exporter      Cross-validate node-exporter collectors"
	@echo "    diagnostics        Collect install-failure artifact bundle"
	@echo "    sol                Run a command on the node via iDRAC SOL"
	@echo "    state-validate     Validate the desired-state YAML"
	@echo "    state-plan         Print the idempotency plan for the state"
	@echo ""
	@echo "  iDRAC operations"
	@echo "    status             Show model, power state, virtual media"
	@echo "    eject              Eject virtual media"
	@echo "    set-boot-cd        Set one-time boot to VirtualCD"
	@echo "    set-boot-hdd       Set one-time boot to HDD"
	@echo "    restart            Force-restart server"
	@echo "    power-on           Power on server"
	@echo "    power-off          Force power off server"
	@echo "    wait-power-on      Wait for power-on state"
	@echo ""
	@echo "  Testing / quality"
	@echo "    test               go test ./... -v"
	@echo "    test-verbose       go test ./... -v"
	@echo "    test-coverage      go test ./... -cover"
	@echo "    fmt                gofmt -w ."
	@echo "    vet                go vet ./..."
	@echo "    lint               gofmt check + go vet"
	@echo ""
	@echo "  Environment / Make variables"
	@echo "    IDRAC_PW           iDRAC password (required for iDRAC ops; never committed)"
	@echo "    IDRAC_IP           iDRAC IP (default 192.168.1.228, from the state)"
	@echo "    IDRAC_USER         iDRAC username (default root, from the state)"
	@echo "    STATE              desired-state YAML (default $(STATE))"
	@echo "    OCP_VERSION        OpenShift version X.Y.Z[-ec/fc/rc.N]"
	@echo "    RELEASE_IMAGE      release image pullspec override (CI nightlies)"
	@echo "    OCP_VERSION / RELEASE_IMAGE and the pacing/retry knobs above are passed through"
	@echo ""
	@echo "  Examples"
	@echo "    make status IDRAC_PW='pass'"
	@echo "    make install IDRAC_PW='pass' OCP_VERSION=5.0.0-rc.2"
	@echo "    make install RELEASE_IMAGE=registry.example.com/ocp-release:5.0.0-ec.6-x86_64"
	@echo "    make day2 phases PHASE2=1   # after a successful install"
	@echo ""

# ---- Build -------------------------------------------------------------------

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/sno-installer
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(MCP_BINARY) ./cmd/sno-mcp
	@echo "Built ./$(BINARY) and ./$(MCP_BINARY) (version $(VERSION))."

build-mcp:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(MCP_BINARY) ./cmd/sno-mcp

tidy:
	$(GO) mod tidy

clean:
	rm -rf workdir/ openshift-install $(BINARY) $(MCP_BINARY)
	rm -rf __pycache__ .pytest_cache htmlcov .coverage
	find . -name '*.pyc' -delete 2>/dev/null || true

# ---- Full workflow -----------------------------------------------------------

# install runs the full end-to-end pipeline. It is idempotent (skips the
# destructive re-provision when a live cluster API is already reachable); delete
# workdir/ to force a clean reinstall.
install: build
	./$(BINARY) install $(OCP_FLAG) $(RELEASE_FLAG) --state $(STATE) $(EXTRA)

# ---- Individual steps --------------------------------------------------------

preflight: build
	./$(BINARY) preflight --state $(STATE)

ssh-key: build
	./$(BINARY) ensure-ssh-key --state $(STATE)

extract-installer: build
	./$(BINARY) extract-installer $(OCP_FLAG) $(RELEASE_FLAG) --state $(STATE)

prepare-configs: build
	./$(BINARY) prepare-configs --state $(STATE)

build-iso: build
	./$(BINARY) build-iso --state $(STATE)

copy-iso: build
	./$(BINARY) copy-iso --state $(STATE)

deploy: build
	./$(BINARY) deploy $(ISO_URL) --state $(STATE)

insert: build
	./$(BINARY) insert $(ISO_URL) --state $(STATE)

wait-install: build
	./$(BINARY) wait-install --state $(STATE)

wait-install-remediate: build
	./$(BINARY) wait-install-maybe-remediate --state $(STATE)

wait-api-ready: build
	./$(BINARY) wait-api-ready --state $(STATE)

remediate-mco: build
	./$(BINARY) remediate-mco --state $(STATE)

# ---- iDRAC operations --------------------------------------------------------

status: build
	./$(BINARY) status --state $(STATE)

eject: build
	./$(BINARY) eject --state $(STATE)

set-boot-cd: build
	./$(BINARY) set-boot-cd --state $(STATE)

set-boot-hdd: build
	./$(BINARY) set-boot-hdd --state $(STATE)

restart: build
	./$(BINARY) restart --state $(STATE)

power-on: build
	./$(BINARY) power-on --state $(STATE)

power-off: build
	./$(BINARY) power-off --state $(STATE)

wait-power-on: build
	./$(BINARY) wait-power-on --state $(STATE)

# ---- Day-2 / operations ------------------------------------------------------

# day2 delegates to the CLI `day2` subcommand. Use DAY2_SUB to select the
# phase, e.g.: make day2 DAY2_SUB="phases --phase1"   (space is significant).
day2: build
	./$(BINARY) day2 $(DAY2_SUB) --state $(STATE)

operator-config: build
	./$(BINARY) day2 operator-config --state $(STATE)

node-exporter: build
	./$(BINARY) node-exporter --state $(STATE)

diagnostics: build
	./$(BINARY) diagnostics --state $(STATE)

sol: build
	./$(BINARY) sol $(SOL_ARGS) --state $(STATE)

# ---- State inspection --------------------------------------------------------

state-validate:
	./$(BINARY) state validate $(STATE)

state-plan:
	./$(BINARY) state plan $(STATE)

# ---- Testing / quality -------------------------------------------------------

test:
	$(GO) test ./... -v

test-verbose:
	$(GO) test ./... -v

test-coverage:
	$(GO) test ./... -cover

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

lint: fmt vet
