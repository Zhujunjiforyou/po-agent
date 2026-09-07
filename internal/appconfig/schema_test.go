package appconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileContainsOnlySelectionAndProviderConnections(t *testing.T) {
	file := New("hub", "https://hub.example/v1", "hub-model")
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"context_window", "max_output_tokens", "capabilities", "parameters", "models"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("config contains runtime field %q: %s", forbidden, data)
		}
	}
}

func TestResolveUsesProviderScopedConnection(t *testing.T) {
	file := New("hub", "https://hub.example/v1", "hub-model")
	file.Providers["local"] = Provider{BaseURL: "http://127.0.0.1:8000/v1", APIKeyEnv: "LOCAL_KEY"}
	runtime, err := file.Resolve(Selection{Provider: "local", Model: "local-model"})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Provider != "local" || runtime.BaseURL != "http://127.0.0.1:8000/v1" || runtime.APIKeyEnv != "LOCAL_KEY" {
		t.Fatalf("runtime = %#v", runtime)
	}
}

func TestLoadLegacyPreservesCurrentModelOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
  "base_url": "https://hub.example/v1",
  "model": "legacy-model",
  "context_window": 262144,
  "model_max_output_tokens": 65536,
  "max_output_tokens": 24576,
  "streaming": true,
  "tools": true,
  "parallel_tool_calls": true,
  "reasoning": false
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := document.Resolve(Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if current.ContextWindow != 262144 || current.MaxOutputTokens != 24576 {
		t.Fatalf("current = %#v", current)
	}
	other, err := document.Resolve(Selection{Model: "other-model"})
	if err != nil {
		t.Fatal(err)
	}
	if other.ContextWindow != DefaultContextWindow || other.MaxOutputTokens != DefaultRequestMaxOutputTokens {
		t.Fatalf("other model inherited legacy settings: %#v", other)
	}
}

func TestWriteUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	if err := Write(path, New("hub", "https://hub.example/v1", "model"), false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("permissions = %o", permissions)
	}
}

func TestWriteRequiresForceToReplaceConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	first := New("hub", "https://first.example/v1", "first")
	if err := Write(path, first, false); err != nil {
		t.Fatal(err)
	}
	second := New("hub", "https://second.example/v1", "second")
	if err := Write(path, second, false); err == nil {
		t.Fatal("Write() unexpectedly replaced an existing config")
	}
	document, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if document.File.Current.Model != "first" {
		t.Fatalf("config changed after rejected write: %#v", document.File)
	}
	if err := Write(path, second, true); err != nil {
		t.Fatal(err)
	}
	document, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if document.File.Current.Model != "second" {
		t.Fatalf("forced config = %#v", document.File)
	}
}

func TestLoadFileRejectsLegacyMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
  "base_url": "https://hub.example/v1",
  "model": "legacy-model",
  "context_window": 131072,
  "model_max_output_tokens": 32768,
  "max_output_tokens": 16384,
  "streaming": true,
  "tools": true,
  "parallel_tool_calls": true,
  "reasoning": false
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("LoadFile() unexpectedly allowed a legacy config mutation")
	}
}

func TestProjectSeparatesSelectionFromRuntimeOverrides(t *testing.T) {
	provider := "local"
	model := "local-model"
	baseURL := "http://127.0.0.1:8000/v1"
	maxOutput := 4096
	temperature := 0.2
	project := Project{
		Provider:        &provider,
		Model:           &model,
		BaseURL:         &baseURL,
		MaxOutputTokens: &maxOutput,
		Temperature:     &temperature,
		ExtraBody:       map[string]any{"top_k": float64(20)},
	}
	selection := project.Select(Selection{Provider: "hub", Model: "hub-model"})
	if selection != (Selection{Provider: "local", Model: "local-model"}) {
		t.Fatalf("selection = %#v", selection)
	}
	runtime := project.Apply(DefaultRuntime("local", Provider{BaseURL: "http://old.example/v1"}, model))
	if runtime.BaseURL != baseURL || runtime.MaxOutputTokens != maxOutput ||
		runtime.Temperature == nil || *runtime.Temperature != temperature {
		t.Fatalf("runtime = %#v", runtime)
	}
	project.ExtraBody["top_k"] = float64(30)
	if runtime.ExtraBody["top_k"] != float64(20) {
		t.Fatal("runtime shares the project extra_body map")
	}
}

func TestFileValidationRejectsBrokenSelections(t *testing.T) {
	tests := []struct {
		name string
		file File
	}{
		{name: "unsupported version", file: File{Version: 2}},
		{name: "empty providers", file: File{Version: Version}},
		{
			name: "unknown current provider",
			file: File{
				Version:   Version,
				Current:   Selection{Provider: "missing", Model: "model"},
				Providers: map[string]Provider{"hub": {BaseURL: "https://hub.example/v1"}},
			},
		},
		{
			name: "empty current model",
			file: File{
				Version:   Version,
				Current:   Selection{Provider: "hub"},
				Providers: map[string]Provider{"hub": {BaseURL: "https://hub.example/v1"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.file.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

func TestResolveAPIKeyUsesDefaultVariableAndRejectsMissingKey(t *testing.T) {
	runtime := DefaultRuntime("hub", Provider{BaseURL: "https://hub.example/v1"}, "model")
	key, err := ResolveAPIKey(runtime, func(name string) string {
		if name == DefaultAPIKeyEnv {
			return "key"
		}
		return ""
	})
	if err != nil || key != "key" {
		t.Fatalf("key = %q, err = %v", key, err)
	}
	if _, err := ResolveAPIKey(runtime, func(string) string { return "" }); err == nil {
		t.Fatal("ResolveAPIKey() unexpectedly accepted a missing key")
	}
}
