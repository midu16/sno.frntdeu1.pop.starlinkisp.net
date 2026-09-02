package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"

	"sno/internal/state"
)

// repoRoot walks up from the current working directory until it finds the
// module root (the directory holding go.mod). go test runs with CWD set to the
// package directory, so this resolves config/ relative to the repo root.
func repoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root (go.mod) not found above CWD")
		}
		dir = parent
	}
}

func TestStrArg(t *testing.T) {
	args := map[string]any{
		"present": "hello",
		"empty":   "",
		"num":     42,
	}
	if got := strArg(args, "present", "def"); got != "hello" {
		t.Errorf("strArg(present) = %q, want hello", got)
	}
	if got := strArg(args, "missing", "def"); got != "def" {
		t.Errorf("strArg(missing) = %q, want def", got)
	}
	if got := strArg(args, "empty", "def"); got != "def" {
		t.Errorf("strArg(empty) = %q, want def (empty falls back)", got)
	}
}

func TestBoolArg(t *testing.T) {
	cases := []struct {
		val  any
		def  bool
		want bool
	}{
		{true, false, true},
		{false, true, false},
		{"true", false, true},
		{"1", false, true},
		{"no", false, false},
		{1.0, false, true},
		{"1.0", false, false}, // string "1.0" is not truthy
	}
	for _, c := range cases {
		args := map[string]any{"flag": c.val}
		if got := boolArg(args, "flag", c.def); got != c.want {
			t.Errorf("boolArg(%v, def=%v) = %v, want %v", c.val, c.def, got, c.want)
		}
	}
}

func TestIntArg(t *testing.T) {
	args := map[string]any{"limit": float64(3), "missing": "x"}
	if got := intArg(args, "limit"); got != 3 {
		t.Errorf("intArg(limit) = %d, want 3", got)
	}
	if got := intArg(args, "missing"); got != 0 {
		t.Errorf("intArg(missing) = %d, want 0", got)
	}
}

func TestToolResultStructure(t *testing.T) {
	res := toolResult("state_validate", "success", map[string]any{"ok": true})
	if res == nil {
		t.Fatal("toolResult returned nil")
	}
	if res.IsError {
		t.Error("expected non-error result")
	}
	// The text payload must contain the wrapped body.
	if len(res.Content) == 0 {
		t.Fatal("expected content")
	}
	if res.StructuredContent == nil {
		t.Error("expected structured content to be populated")
	}
}

func TestErrResultIsError(t *testing.T) {
	res := errResult("sno_install", context.DeadlineExceeded)
	if !res.IsError {
		t.Error("expected error result to have IsError set")
	}
}

func TestListToolsNonEmpty(t *testing.T) {
	tools := listTools()
	if len(tools) != 5 {
		t.Errorf("listTools() = %d tools, want 5", len(tools))
	}
	want := map[string]bool{
		"sno_install": false, "state_validate": false, "state_plan": false,
		"ocp_versions": false, "ocp_latest": false,
	}
	for _, tool := range tools {
		if _, ok := want[tool.Tool.Name]; ok {
			want[tool.Tool.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("expected tool %q in listTools", name)
		}
	}
}

func TestStateValidateHandler(t *testing.T) {
	// The repo ships a valid state document; validate it through the handler.
	stateFile := filepath.Join(repoRoot(t), "config", "sno-state.yaml")
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "state_validate",
			Arguments: map[string]any{"state_file": stateFile},
		},
	}
	res, err := handleStateValidate(context.Background(), req)
	if err != nil {
		t.Fatalf("handleStateValidate error: %v", err)
	}
	if res.IsError {
		t.Fatalf("handleStateValidate returned error result: %v", res.Content)
	}
	// Structured content should carry the validated identity under "result".
	body, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatal("structured content is not a map")
	}
	if body["tool"] != "state_validate" {
		t.Errorf("tool = %v, want state_validate", body["tool"])
	}
	if body["status"] != "success" {
		t.Errorf("status = %v, want success", body["status"])
	}
	// The payload is the stateValidateOutput struct itself.
	result, ok := body["result"].(stateValidateOutput)
	if !ok {
		t.Fatalf("result payload is not stateValidateOutput: %T", body["result"])
	}
	if result.Cluster != "sno" {
		t.Errorf("cluster = %q, want sno", result.Cluster)
	}
	if result.APIVersion != state.APIVersion {
		t.Errorf("apiVersion = %q, want %q", result.APIVersion, state.APIVersion)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "third"); got != "third" {
		t.Errorf("firstNonEmpty = %q, want third", got)
	}
	if got := firstNonEmpty("first", "second"); got != "first" {
		t.Errorf("firstNonEmpty = %q, want first", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("firstNonEmpty() = %q, want empty", got)
	}
}

// Ensure the output structs marshal to JSON (guards against struct tag typos).
func TestOutputStructsMarshal(t *testing.T) {
	payload := stateValidateOutput{
		File:           "config/sno-state.yaml",
		APIVersion:     state.APIVersion,
		Kind:           state.Kind,
		Cluster:        "sno",
		BaseDomain:     "example.com",
		Version:        "5.0.0-ec.6",
		NodeIP:         "192.168.1.133",
		IDrac:          "192.168.1.228",
		Webcache:       "192.168.1.21",
		MachineConfigs: 18,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal stateValidateOutput: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty json")
	}
}
