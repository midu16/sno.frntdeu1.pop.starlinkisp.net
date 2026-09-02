---
name: sno-mcp
description: Drive the Golang-native SNO installer through MCP tools (sno-installer server). Use when asked to install/reinstall/reprovision the SNO cluster, validate the desired-state YAML, inspect the idempotency plan, list the supported OpenShift versions, or trigger a full compliant SNO install with a single tool call. Prefer this over the legacy idrac_sushy.py driver.
---

# SNO installer via MCP

The SNO installer is exposed as MCP tools by the stdio server `bin/sno-mcp`
(built from `cmd/sno-mcp`). It runs the same orchestration as the CLI with no
legacy Python/Bash and no `os/exec` of old scripts. The single source of truth
for a compliant install is the desired-state YAML at `config/sno-state.yaml`.

> **Destructive.** `sno_install` re-provisions the node. The user asking to
> install/reprovision IS the authorization. Read-only tools (`state_validate`,
> `state_plan`, `ocp_versions`, `ocp_latest`) are always safe.

## When to use which tool

1. **Trigger a full install** → `sno_install` with `state_file: config/sno-state.yaml`.
   One call runs the whole idempotent pipeline. Add `dry_run: true` for a
   read-only plan first.
2. **Validate the state file** → `state_validate` (or `make state/validate`).
3. **See what will happen** → `state_plan` (read-only idempotency plan).
4. **Pick a version** → `ocp_versions` (list GA versions) or `ocp_latest`.

## Prerequisites

- The server binary is built: `make bin/sno-mcp` (or `go build -o bin/sno-mcp ./cmd/sno-mcp`).
- The client has the `sno-installer` MCP server registered (see `.mcp.json`).
- The state file exists and is valid: `make state/validate` or `sno_install` with
  `dry_run: true`.

## Typical flow

1. `state_validate` on `config/sno-state.yaml` to confirm cluster identity,
   base domain, version, node IP, iDRAC, and webcache.
2. `state_plan` to review which stages run/skip/verify and any warnings
   (missing pull secret, existing cluster, destructive re-provision).
3. `ocp_versions` to confirm the target version is still in the catalog.
4. `sno_install` (dry run first, then real) with `state_file`.
5. Monitor via structured logs on stderr; do not fabricate the outcome.

## Arguments

- `sno_install`: `state_file` (required), `dry_run`, `ocp_version`,
  `release_image`, `iso_url`, `workdir`.
- `state_validate` / `state_plan`: `state_file`.
- `ocp_versions`: `limit`. `ocp_latest`: none.

## Idempotency

Every run is safe to repeat. The installer short-circuits when a prior run's
kubeconfig points at a live API — a node that already carries the requested
OpenShift is detected and left untouched. To force a full reinstall, delete the
workdir first.

## Security

- No secrets are embedded in the state file — pull secret, SSH key, and iDRAC
  password are referenced by path or environment.
- Never print `/home/midu/config.json`, `IDRAC_PW`, or kubeadmin creds.

## Reference

- Server source: `cmd/sno-mcp/`
- Tool schemas: `mcp/` (regenerate with `SNO_MCP_SCHEMAS=mcp ./bin/sno-mcp`)
- Client config: `.mcp.json`
- Full docs: `docs/mcp.md`
