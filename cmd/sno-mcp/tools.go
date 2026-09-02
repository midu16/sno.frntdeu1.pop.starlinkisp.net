package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"sno/internal/ocp"
	"sno/internal/sno"
	"sno/internal/state"
)

// ---------------------------------------------------------------------------
// Tool result helpers
// ---------------------------------------------------------------------------

// toolResult wraps a machine-readable payload as an MCP text result and also
// populates StructuredContent so clients that request structured output can
// parse it deterministically instead of scraping prose.
func toolResult(name string, status string, payload any) *mcp.CallToolResult {
	body := map[string]any{"tool": name, "status": status}
	if payload != nil {
		body["result"] = payload
	}
	data, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("encode result: %v", err))
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{mcp.TextContent{Type: "text", Text: string(data)}},
		StructuredContent: body,
	}
}

// errResult renders a tool error as an MCP error result.
func errResult(name string, err error) *mcp.CallToolResult {
	return mcp.NewToolResultErrorFromErr(fmt.Sprintf("%s failed: %v", name, err), nil)
}

// ---------------------------------------------------------------------------
// Argument extraction
// ---------------------------------------------------------------------------

// strArg reads a string argument with a fallback default.
func strArg(args any, key, def string) string {
	m, _ := args.(map[string]any)
	if v, ok := m[key].(string); ok {
		if s := trimSpace(v); s != "" {
			return s
		}
	}
	return def
}

// boolArg reads a boolean argument, returning def when absent or unparseable.
func boolArg(args any, key string, def bool) bool {
	m, _ := args.(map[string]any)
	v, ok := m[key]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1" || t == "yes" || t == "on"
	case float64:
		return t == 1
	}
	return def
}

// intArg reads a numeric argument.
func intArg(args any, key string) int {
	m, _ := args.(map[string]any)
	switch t := m[key].(type) {
	case float64:
		return int(t)
	case int:
		return t
	}
	return 0
}

// trimSpace trims surrounding whitespace and expands env references.
func trimSpace(s string) string {
	return os.ExpandEnv(s)
}

// ---------------------------------------------------------------------------
// sno_install — the headline tool
// ---------------------------------------------------------------------------

// installInput is the schema for the sno_install tool. StateFile defaults to
// the conventional desired-state document; the remaining fields let an agent
// override the resolved config without editing the state file.
type installInput struct {
	StateFile    string `json:"state_file"`
	DryRun       bool   `json:"dry_run,omitempty"`
	ReleaseImage string `json:"release_image,omitempty"`
	OcpVersion   string `json:"ocp_version,omitempty"`
	ISOURL       string `json:"iso_url,omitempty"`
	WorkDir      string `json:"workdir,omitempty"`
}

// newInstallTool builds the sno_install tool: read the desired-state YAML and
// (unless dry_run) trigger the full, idempotent SNO install in one call.
func newInstallTool() server.ServerTool {
	tool := mcp.NewTool(
		"sno_install",
		mcp.WithToolTitle("SNO Install"),
		mcp.WithDescription(
			"Trigger the full Single Node OpenShift (SNO) installation from a desired-state "+
				"YAML document. Reads the state file (source of truth for cluster identity, "+
				"network plan and OpenShift version), resolves iDRAC/webcache/pull-secret "+
				"references, then runs the idempotent install pipeline (preflight -> ssh-key -> "+
				"extract -> prepare-configs -> build-iso -> copy-iso -> iDRAC deploy -> wait-install). "+
				"Re-running is safe: a node that already carries the requested OpenShift is detected "+
				"and left untouched. Use dry_run=true for a read-only validation + idempotency plan.",
		),
		mcp.WithInputSchema[installInput](),
		mcp.WithOutputSchema[snoInstallOutput](),
	)
	return server.ServerTool{Tool: tool, Handler: handleInstall}
}

func handleInstall(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	stateFile := strArg(args, "state_file", defaultStateFile)
	dryRun := boolArg(args, "dry_run", false)

	st, err := state.Load(stateFile)
	if err != nil {
		return errResult("sno_install", err), nil
	}

	cfg, err := sno.FromState(st)
	if err != nil {
		return errResult("sno_install", err), nil
	}
	// Agent-supplied overrides take precedence over the resolved state values.
	if v := strArg(args, "ocp_version", ""); v != "" {
		cfg.OcpVersion = v
	}
	if v := strArg(args, "release_image", ""); v != "" {
		cfg.ReleaseImage = v
	}
	if v := strArg(args, "iso_url", ""); v != "" {
		cfg.IsoURL = v
	}
	if v := strArg(args, "workdir", ""); v != "" {
		cfg.WorkDir = v
	}

	inst := sno.NewWithLogger(ctx, cfg, sno.DefaultLogger())
	inst.AttachState(st)

	start := time.Now()
	installErr := inst.Install(sno.WithDryRun(dryRun))
	elapsed := time.Since(start)

	out := snoInstallOutput{
		ClusterName:  st.Metadata.Name,
		BaseDomain:   st.Metadata.BaseDomain,
		OcpVersion:   cfg.OcpVersion,
		ReleaseImage: cfg.ReleaseImage,
		DryRun:       dryRun,
		ElapsedSec:   elapsed.Round(time.Millisecond).Seconds(),
	}
	if installErr != nil {
		out.Error = installErr.Error()
		return errResult("sno_install", installErr), nil
	}

	if dryRun {
		plan, pErr := inst.PlanForState(st)
		if pErr == nil {
			out.Plan = plan
		}
	}
	return toolResult("sno_install", "success", out), nil
}

// snoInstallOutput is the JSON projection returned by sno_install.
type snoInstallOutput struct {
	ClusterName  string    `json:"clusterName"`
	BaseDomain   string    `json:"baseDomain"`
	OcpVersion   string    `json:"ocpVersion"`
	ReleaseImage string    `json:"releaseImage,omitempty"`
	DryRun       bool      `json:"dryRun"`
	ElapsedSec   float64   `json:"elapsedSec"`
	Plan         *sno.Plan `json:"plan,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// state_validate — read-only validation of a desired-state document
// ---------------------------------------------------------------------------

// newStateValidateTool builds the state_validate tool.
func newStateValidateTool() server.ServerTool {
	tool := mcp.NewTool(
		"state_validate",
		mcp.WithToolTitle("Validate Desired State"),
		mcp.WithDescription(
			"Validate a Single Node OpenShift desired-state YAML document against the "+
				"sno.infra/v1 schema. Read-only: safe to call from CI. Returns the resolved "+
				"cluster identity, base domain, version and node IP.",
		),
		mcp.WithInputSchema[stateValidateInput](),
		mcp.WithOutputSchema[stateValidateOutput](),
	)
	return server.ServerTool{Tool: tool, Handler: handleStateValidate}
}

type stateValidateInput struct {
	StateFile string `json:"state_file"`
}

type stateValidateOutput struct {
	File           string `json:"file"`
	APIVersion     string `json:"apiVersion"`
	Kind           string `json:"kind"`
	Cluster        string `json:"cluster"`
	BaseDomain     string `json:"baseDomain"`
	Version        string `json:"version"`
	NodeIP         string `json:"nodeIP"`
	IDrac          string `json:"idrac"`
	Webcache       string `json:"webcache"`
	MachineConfigs int    `json:"machineConfigs"`
}

func handleStateValidate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	stateFile := strArg(args, "state_file", defaultStateFile)
	st, err := state.Load(stateFile)
	if err != nil {
		return errResult("state_validate", err), nil
	}
	if err := st.Validate(); err != nil {
		return errResult("state_validate", err), nil
	}
	out := stateValidateOutput{
		File:           stateFile,
		APIVersion:     st.APIVersion,
		Kind:           st.Kind,
		Cluster:        st.Metadata.Name,
		BaseDomain:     st.Metadata.BaseDomain,
		Version:        firstNonEmpty(st.Spec.Openshift.Version, st.Spec.Openshift.ReleaseImage),
		NodeIP:         st.Spec.Networking.NodeIP,
		IDrac:          st.Spec.IDrac.Host,
		Webcache:       st.Spec.Webcache.Host,
		MachineConfigs: len(st.Spec.MachineConfigs),
	}
	return toolResult("state_validate", "success", out), nil
}

// ---------------------------------------------------------------------------
// state_plan — read-only idempotency plan
// ---------------------------------------------------------------------------

// newStatePlanTool builds the state_plan tool.
func newStatePlanTool() server.ServerTool {
	tool := mcp.NewTool(
		"state_plan",
		mcp.WithToolTitle("State Idempotency Plan"),
		mcp.WithDescription(
			"Generate the read-only idempotency plan for a desired-state document: which "+
				"install stages will run, skip or verify, plus warnings (missing pull secret, "+
				"existing cluster, etc.). Safe on any machine.",
		),
		mcp.WithInputSchema[statePlanInput](),
	)
	return server.ServerTool{Tool: tool, Handler: handleStatePlan}
}

type statePlanInput struct {
	StateFile string `json:"state_file"`
}

func handleStatePlan(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	stateFile := strArg(args, "state_file", defaultStateFile)
	st, err := state.Load(stateFile)
	if err != nil {
		return errResult("state_plan", err), nil
	}
	// Force no-network mode so the plan is reproducible on any machine,
	// matching the CLI's --dry-run behaviour.
	_ = os.Setenv("SNO_PREFER_NO_NETWORK", "1")
	inst := sno.NewWithLogger(ctx, mustFromState(st), sno.DefaultLogger())
	inst.AttachState(st)
	plan, err := inst.PlanForState(st)
	if err != nil {
		return errResult("state_plan", err), nil
	}
	return toolResult("state_plan", "success", plan), nil
}

// ---------------------------------------------------------------------------
// ocp_versions — dynamic OpenShift version catalog
// ---------------------------------------------------------------------------

// newOCPVersionsTool builds the ocp_versions tool.
func newOCPVersionsTool() server.ServerTool {
	tool := mcp.NewTool(
		"ocp_versions",
		mcp.WithToolTitle("OpenShift Versions"),
		mcp.WithDescription(
			"List the OpenShift versions currently supported by the Red Hat version "+
				"catalog (https://api.openshift.com/products/openshift). Read-only: drives the "+
				"CI version matrix and lets an agent pick a compliant version.",
		),
		mcp.WithInputSchema[ocpVersionsInput](),
		mcp.WithOutputSchema[ocpVersionsOutput](),
	)
	return server.ServerTool{Tool: tool, Handler: handleOCPVersions}
}

type ocpVersionsInput struct {
	Limit int `json:"limit"`
}

type ocpVersionsOutput struct {
	Count    int                 `json:"count"`
	Versions []ocpVersionSummary `json:"versions"`
}

type ocpVersionSummary struct {
	Version  string `json:"version"`
	Channel  string `json:"channel"`
	PullSpec string `json:"pullspec"`
}

func handleOCPVersions(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	client := ocp.NewRegistryClient()
	versions, err := client.Supported(ctx)
	if err != nil {
		return errResult("ocp_versions", err), nil
	}
	if limit := intArg(args, "limit"); limit > 0 && limit < len(versions) {
		versions = versions[:limit]
	}
	out := ocpVersionsOutput{Count: len(versions)}
	for _, v := range versions {
		out.Versions = append(out.Versions, ocpVersionSummary{
			Version:  v.Raw,
			Channel:  string(v.Channel),
			PullSpec: v.DefaultPullSpec(),
		})
	}
	return toolResult("ocp_versions", "success", out), nil
}

// ---------------------------------------------------------------------------
// ocp_latest — newest GA version from the catalog
// ---------------------------------------------------------------------------

// newOCPLatestTool builds the ocp_latest tool.
func newOCPLatestTool() server.ServerTool {
	tool := mcp.NewTool(
		"ocp_latest",
		mcp.WithToolTitle("Latest OpenShift Version"),
		mcp.WithDescription(
			"Return the newest GA OpenShift version from the Red Hat version catalog, "+
				"including its quay pullspec. Read-only.",
		),
		mcp.WithInputSchema[emptyInput](),
		mcp.WithOutputSchema[ocpLatestOutput](),
	)
	return server.ServerTool{Tool: tool, Handler: handleOCPLatest}
}

type emptyInput struct{}

type ocpLatestOutput struct {
	Version  string `json:"version"`
	Channel  string `json:"channel"`
	PullSpec string `json:"pullspec"`
}

func handleOCPLatest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client := ocp.NewRegistryClient()
	v, err := client.Latest(ctx)
	if err != nil {
		return errResult("ocp_latest", err), nil
	}
	out := ocpLatestOutput{Version: v.Raw, Channel: string(v.Channel), PullSpec: v.DefaultPullSpec()}
	return toolResult("ocp_latest", "success", out), nil
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

// mustFromState is the FromState wrapper used by read-only tools that must not
// fail on an unresolved release image (the plan degrades gracefully).
func mustFromState(st *state.SNOClusterState) sno.Config {
	c, err := sno.FromState(st)
	if err != nil {
		// Fall back to an unresolved config; PlanForState only needs the
		// identity fields and tolerates a missing release image.
		return sno.Config{}
	}
	return c
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// ensure slog stays referenced if the logger surface changes.
var _ = slog.LevelInfo
