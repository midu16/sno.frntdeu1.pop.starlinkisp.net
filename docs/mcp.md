# sno-mcp — SNO Installer as MCP Tools

`sno-mcp` is the Model Context Protocol server that exposes the Golang-native SNO
installer (`bin/sno-mcp`, built from `cmd/sno-mcp`) as a set of tools an AI agent
can call over a stdio transport. It runs the **same** orchestration as
`bin/sno-installer` — no legacy Python/Bash, no `os/exec` of old scripts.
`os/exec` is used only to run `oc` / `openshift-install` against the target node.

## Why

The single source of truth for a compliant install is the desired-state YAML
(`config/sno-state.yaml`). The headline tool, `sno_install`, reads that document
and (unless `dry_run`) triggers the **full, idempotent** SNO install in one tool
call. An agent that has read the state file can therefore drive an entire
install with a single call.

## Transport & logging

- Stdio transport. **stdout is reserved for the MCP JSON-RPC protocol.**
- All logs go to **stderr** (`SNO_LOG_FILE=/dev/stderr` is set by the client
  config in `.mcp.json`). Configure level/format with `SNO_LOG_LEVEL`
  (`debug|info|warn|error`) and `SNO_LOG_FORMAT` (`text|json`).
- Graceful shutdown on `SIGINT`/`SIGTERM`.

## Tools

| Tool | Kind | Purpose |
|------|------|---------|
| `sno_install` | read/write | Trigger the full idempotent install from a state file. Arguments: `state_file` (required), `dry_run`, `ocp_version`, `release_image`, `iso_url`, `workdir`. |
| `state_validate` | read | Validate a desired-state YAML against the `sno.infra/v1` schema. Args: `state_file`. |
| `state_plan` | read | Read-only idempotency plan: which stages run/skip/verify + warnings. Args: `state_file`. |
| `ocp_versions` | read | List GA OpenShift versions from the Red Hat catalog. Args: `limit`. |
| `ocp_latest` | read | Newest GA version + quay pullspec. No args. |

Every result is returned as MCP text **and** `structuredContent` (a JSON object
with `tool`, `status`, and `result`), so a client can parse the outcome
deterministically.

## Idempotency

Every run is safe to repeat. The installer short-circuits when a kubeconfig from
a prior run points at a live API — a node that already carries the requested
OpenShift is detected and left untouched. To force a full reinstall, delete the
workdir first.

## Connecting a client

### Claude Code (project)

Add to `.mcp.json` at the repo root:

```json
{
  "mcpServers": {
    "sno-installer": {
      "command": "bin/sno-mcp",
      "args": [],
      "env": {
        "SNO_LOG_FILE": "/dev/stderr",
        "SNO_LOG_LEVEL": "info",
        "SNO_STATE_FILE": "config/sno-state.yaml"
      }
    }
  }
}
```

Then restart Claude Code; the tools appear automatically.

### Claude Desktop / generic stdio clients

Same `.mcp.json`, or pass the server via CLI. Point `command` at the built
`bin/sno-mcp` (or `make bin/sno-mcp` / `go build -o bin/sno-mcp ./cmd/sno-mcp`).

## Regenerating tool schemas

The JSON/YAML schema artifacts under `mcp/` are generated from the tools' real
declared schemas — they cannot drift. Regenerate with:

```bash
SNO_MCP_SCHEMAS=mcp ./bin/sno-mcp
```

This writes `<tool>.json` and `<tool>.yml` per tool plus a combined
`mcp/tools.json` index.

## Security

- No secrets are embedded in the state file — pull secret, SSH key, and iDRAC
  password are referenced by path or environment.
- Never print `/home/midu/config.json`, `IDRAC_PW`, or kubeadmin creds.
- `sno_install` is destructive (it re-provisions the node); `state_validate`,
  `state_plan`, `ocp_versions`, and `ocp_latest` are read-only.
