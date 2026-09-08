package main

import (
	"bytes"
	"path/filepath"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/internal/appconfig"
)

func TestRiskyToolReasonsCoverEveryProcessAndMutationTool(t *testing.T) {
	reasons := riskyToolReasons()
	for _, name := range []string{"edit", "git", "go", "shell", "write"} {
		if reasons[name] == "" {
			t.Errorf("tool %q has no approval reason", name)
		}
	}
}

func TestAppResourceClaimsLeavesCalculatorUnconstrained(t *testing.T) {
	call, err := po.NewToolCall("call-1", "calculator", map[string]any{
		"operation": "add",
		"a":         1,
		"b":         2,
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := appResourceClaims(call)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("calculator claims = %#v, want none", claims)
	}
}

func TestREPLModelSwitchReplacesOnlyAgent(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	output := newPlainModelOutput(&stdout, &stderr)
	initial := appconfig.DefaultRuntime("provider-a", appconfig.Provider{BaseURL: "http://provider-a.example/v1"}, "model-a")
	runtime, err := buildAppRuntime(initial, "key-a", runtimeOptions{
		Workspace: t.TempDir(),
		Approver:  staticApprover(false),
		Output:    output,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	workspace := runtime.Workspace
	agent := runtime.Agent
	next := appconfig.DefaultRuntime("provider-b", appconfig.Provider{BaseURL: "http://provider-b.example/v1"}, "model-b")
	next.APIKeyEnv = "PROVIDER_B_KEY"
	t.Setenv("PROVIDER_B_KEY", "key-b")
	configPath := filepath.Join(t.TempDir(), "config.json")
	file := appconfig.New("provider-a", initial.BaseURL, initial.Model)
	file.Providers["provider-b"] = appconfig.Provider{BaseURL: next.BaseURL, APIKeyEnv: next.APIKeyEnv}
	if err := appconfig.Write(configPath, file, false); err != nil {
		t.Fatal(err)
	}
	manager := newREPLModelManager(configPath, initial, runtime, nil)
	if err := manager.Switch(modelChoice{Provider: "provider-b", Model: "model-b", config: next}); err != nil {
		t.Fatal(err)
	}
	if runtime.Agent == agent {
		t.Fatal("model switch reused the old Agent")
	}
	if runtime.Workspace != workspace {
		t.Fatal("model switch replaced the workspace")
	}
	if current := manager.Current(); current.Model != "model-b" || current.BaseURL != next.BaseURL {
		t.Fatalf("current model = %#v", current)
	}
	updated, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Current != (appconfig.Selection{Provider: "provider-b", Model: "model-b"}) {
		t.Fatalf("persisted model = %#v", updated.Current)
	}
}

func TestREPLModelSwitchDoesNotPartiallyApplyWhenPersistenceFails(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	initial := appconfig.DefaultRuntime("first", appconfig.Provider{BaseURL: "http://first.example/v1"}, "model-a")
	runtime, err := buildAppRuntime(initial, "key-a", runtimeOptions{
		Workspace: t.TempDir(),
		Approver:  staticApprover(false),
		Output:    newPlainModelOutput(&stdout, &stderr),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	originalAgent := runtime.Agent
	next := appconfig.DefaultRuntime("second", appconfig.Provider{BaseURL: "http://second.example/v1"}, "model-b")
	t.Setenv(appconfig.DefaultAPIKeyEnv, "key-b")
	manager := newREPLModelManager(filepath.Join(t.TempDir(), "missing.json"), initial, runtime, nil)
	err = manager.Switch(modelChoice{Provider: "second", Model: "model-b", config: next})
	if err == nil {
		t.Fatal("Switch() unexpectedly succeeded")
	}
	if runtime.Agent != originalAgent || manager.Current().Model != "model-a" {
		t.Fatal("failed switch partially changed the active model")
	}
}
