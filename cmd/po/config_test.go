package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemonzjj/po-agent-go/internal/appconfig"
)

func testAppConfig(baseURL, model string) appconfig.Runtime {
	return appconfig.DefaultRuntime(
		"default",
		appconfig.Provider{BaseURL: baseURL, APIKeyEnv: appconfig.DefaultAPIKeyEnv},
		model,
	)
}

func writeRuntimeConfig(t *testing.T, path string, runtime appconfig.Runtime) {
	t.Helper()
	provider := runtime.Provider
	if provider == "" {
		provider = "default"
	}
	file := appconfig.New(provider, runtime.BaseURL, runtime.Model)
	file.Providers[provider] = appconfig.Provider{BaseURL: runtime.BaseURL, APIKeyEnv: runtime.APIKeyEnv}
	if err := appconfig.Write(path, file, false); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigRejectsUnknownAndTrailingValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeRuntimeConfig(t, path, testAppConfig("https://example.com/v1", "model"))
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(valid))
	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown field", data: []byte(strings.TrimSuffix(base, "}") + `,"unknown":true}`)},
		{name: "trailing value", data: []byte(base + "\n{}\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := appconfig.Load(path); err == nil {
				t.Fatal("Load() unexpectedly succeeded")
			}
		})
	}
}

func TestVersionedConfigSelectsModelWithinProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	file := appconfig.New("hub", "https://hub.example/v1", "hub-model")
	file.Providers["local"] = appconfig.Provider{
		BaseURL:   "http://127.0.0.1:8000/v1",
		APIKeyEnv: "LOCAL_MODEL_KEY",
	}
	if err := appconfig.Write(path, file, false); err != nil {
		t.Fatal(err)
	}
	document, err := appconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := document.Resolve(appconfig.Selection{Provider: "local", Model: "local-model"})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Provider != "local" || runtime.Model != "local-model" || runtime.APIKeyEnv != "LOCAL_MODEL_KEY" {
		t.Fatalf("selected runtime = %#v", runtime)
	}
}

func TestLegacyConfigRetainsItsRuntimeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
  "base_url": "https://hub.example/v1",
  "model": "legacy-model",
  "api_key_env": "PO_API_KEY",
  "context_window": 262144,
  "model_max_output_tokens": 32768,
  "max_output_tokens": 24576,
  "streaming": true,
  "tools": true,
  "parallel_tool_calls": true,
  "reasoning": false
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := appconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := document.Resolve(appconfig.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if !document.IsLegacy() || runtime.ContextWindow != 262144 || runtime.MaxOutputTokens != 24576 {
		t.Fatalf("legacy runtime = %#v", runtime)
	}
	other, err := document.Resolve(appconfig.Selection{Model: "other-model"})
	if err != nil {
		t.Fatal(err)
	}
	if other.ContextWindow != appconfig.DefaultContextWindow {
		t.Fatalf("new model inherited legacy limits: %#v", other)
	}
}

func TestResolveAPIKeyUsesSelectedProviderEnvironment(t *testing.T) {
	t.Setenv(appconfig.DefaultAPIKeyEnv, "default-key")
	t.Setenv("SECOND_PROVIDER_KEY", "second-key")
	runtime := appconfig.DefaultRuntime(
		"second",
		appconfig.Provider{BaseURL: "https://second.example/v1", APIKeyEnv: "SECOND_PROVIDER_KEY"},
		"model",
	)
	key, err := appconfig.ResolveAPIKey(runtime, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if key != "second-key" {
		t.Fatalf("API key = %q, want provider-specific key", key)
	}
}
