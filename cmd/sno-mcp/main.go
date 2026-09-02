// Command sno-mcp is the Model Context Protocol server for the SNO installer.
//
// It exposes the same Go orchestration logic as sno-installer (the native
// replacements for idrac_sushy.py, sol_console.py and the day-2 driver) as a
// set of MCP tools an AI agent can invoke over a stdio transport. The server
// never shells out to the legacy scripts — every tool drives internal/*
// packages directly, and os/exec is used only to run oc / openshift-install
// against the target node.
//
// Logging goes to stderr; stdout is reserved for the JSON-RPC protocol, so the
// server honours SNO_LOG_FILE=/dev/stderr (the default when running under an
// MCP client) and SNO_LOG_LEVEL / SNO_LOG_FORMAT like the CLI.
//
// The headline tool is sno_install: an agent reads the desired-state YAML (the
// single source of truth for a compliant install) and issues one tool call to
// trigger the full pipeline. Every run is idempotent — a node that already
// carries the requested OpenShift is detected and left untouched.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mark3labs/mcp-go/server"
	"gopkg.in/yaml.v3"
)

// listTools returns the fully wired tool definitions for the server.
func listTools() []server.ServerTool {
	return []server.ServerTool{
		newInstallTool(),
		newStateValidateTool(),
		newStatePlanTool(),
		newOCPVersionsTool(),
		newOCPLatestTool(),
	}
}

// defaultStateFile is the conventional desired-state document. The CLI uses the
// same path so an agent and a human resolve to the same compliance source.
const defaultStateFile = "config/sno-state.yaml"

func main() {
	// MCP stdio uses stdout for the protocol; all human / machine logs must
	// go to stderr. Route the process logger there unless SNO_LOG_FILE
	// overrides it.
	if os.Getenv("SNO_LOG_FILE") == "" {
		_ = os.Setenv("SNO_LOG_FILE", "/dev/stderr")
	}

	// Generate the tool schema artifacts instead of starting the stdio server.
	// This is a build-time/dev-time convenience so the JSON/YAML schemas stay
	// in lockstep with the tools' real declarations.
	if outDir := os.Getenv("SNO_MCP_SCHEMAS"); outDir != "" {
		if err := writeSchemas(outDir); err != nil {
			fmt.Fprintln(os.Stderr, "schema generation failed:", err)
			os.Exit(1)
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// handleInterrupt turns SIGINT/SIGTERM into a graceful context cancel so
	// the stdio server exits cleanly on shutdown.
	handleInterrupt(cancel)

	srv := server.NewMCPServer("sno-installer", "0.1.0", server.WithRecovery())
	tools := listTools()
	for _, t := range tools {
		srv.AddTool(t.Tool, t.Handler)
	}

	log.New(os.Stderr, "[sno-mcp] ", log.LstdFlags).Printf(
		"ready: %d tools, default_state=%s", len(tools), defaultStateFile,
	)

	stdio := server.NewStdioServer(srv)
	if err := stdio.Listen(ctx, os.Stdin, os.Stdout); err != nil {
		log.SetOutput(os.Stderr)
		log.Printf("sno-mcp exited with error: %v", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// writeSchemas emits one JSON schema document per tool plus a combined index
// into outDir, using the tools' real declared schemas so the artifacts cannot
// drift from the server.
func writeSchemas(outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	tools := listTools()
	index := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		name := t.Tool.Name
		entry := map[string]any{
			"name":        name,
			"title":       t.Tool.Title,
			"description": t.Tool.Description,
		}
		// Persist the input schema (raw when present, else the generated one).
		inSchema := t.Tool.RawInputSchema
		if len(inSchema) == 0 {
			if b, err := json.Marshal(t.Tool.InputSchema); err == nil {
				inSchema = b
			}
		}
		if len(inSchema) > 0 {
			entry["inputSchema"] = json.RawMessage(inSchema)
		}
		if err := writeFile(outDir, name+".json", inSchema); err != nil {
			return err
		}
		// YAML is a friendlier artifact for hand-editing and docs.
		if yml, err := yaml.Marshal(jsonToYAML(inSchema)); err == nil {
			if err := writeFile(outDir, name+".yml", yml); err != nil {
				return err
			}
		}
		index = append(index, entry)
	}
	idxJSON, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(outDir, "tools.json", idxJSON); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %d tool schemas to %s\n", len(tools), outDir)
	return nil
}

// writeFile writes data to outDir/name, creating the directory if needed.
func writeFile(outDir, name string, data []byte) error {
	path := outDir + "/" + name
	return os.WriteFile(path, data, 0o644)
}

// jsonToYAML re-encodes a JSON value as an interface tree so it can be
// marshalled as YAML. json.RawMessage round-trips through a generic any.
func jsonToYAML(raw []byte) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

// handleInterrupt cancels ctx on the first SIGINT/SIGTERM so the stdio server
// shuts down gracefully rather than being force-killed mid tool call.
func handleInterrupt(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
}
