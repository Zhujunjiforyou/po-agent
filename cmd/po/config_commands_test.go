package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/internal/modelcatalog"
)

func TestConfigHelpListsEveryPublicCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runConfig(context.Background(), []string{"--help"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, command := range []string{
		"config init", "config add-provider", "config models", "config use",
		"config show", "config path", "config migrate",
	} {
		if !strings.Contains(stdout.String(), command) {
			t.Errorf("config help missing %q: %q", command, stdout.String())
		}
	}
}

func TestConfigModelsShowsMissingMetadataWithoutGuessing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"aliyun/kimi-k3"}]}`)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	file := appconfig.New("hub", server.URL+"/v1", "aliyun/kimi-k3")
	if err := appconfig.Write(path, file, false); err != nil {
		t.Fatal(err)
	}
	previous := catalogClient
	catalogClient = modelcatalog.Client{HTTP: server.Client()}
	t.Cleanup(func() { catalogClient = previous })

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runConfigModels(context.Background(), []string{"--config", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	line := stdout.String()
	if !strings.Contains(line, "aliyun/kimi-k3") || !strings.Contains(line, "-        -") {
		t.Fatalf("output = %q", line)
	}
}

func TestConfigShowListsConfiguredProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	file := appconfig.New("hub", "https://hub.example/v1", "hub-model")
	file.Providers["backup"] = appconfig.Provider{BaseURL: "https://backup.example/v1"}
	if err := appconfig.Write(path, file, false); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runConfigShow([]string{"--config", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "providers: backup, hub\n") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestConfigUsePersistsProviderAndRawModelID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	file := appconfig.New("hub", "https://hub.example/v1", "base-model")
	file.Providers["local"] = appconfig.Provider{BaseURL: "http://127.0.0.1:8000/v1"}
	if err := appconfig.Write(path, file, false); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runConfigUse(
		[]string{"--config", path, "--provider", "local", "local-model"},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	updated, err := appconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := appconfig.Selection{Provider: "local", Model: "local-model"}
	if updated.Current != want {
		t.Fatalf("current = %#v, want %#v", updated.Current, want)
	}
}

func TestConfigAddProviderStoresConnectionWithoutSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := appconfig.Write(path, appconfig.New("hub", "https://hub.example/v1", "base-model"), false); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runConfigAddProvider([]string{
		"--config", path,
		"--name", "local",
		"--base-url", "http://127.0.0.1:8000/v1",
		"--api-key-env", "LOCAL_MODEL_KEY",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	updated, err := appconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := updated.Providers["local"]
	if provider.BaseURL != "http://127.0.0.1:8000/v1" || provider.APIKeyEnv != "LOCAL_MODEL_KEY" {
		t.Fatalf("provider = %#v", provider)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "api_key\"") {
		t.Fatalf("config unexpectedly contains a raw API key field: %s", data)
	}
}

func TestConfigMigrateCreatesBackupAndMinimalConfig(t *testing.T) {
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
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runConfigMigrate([]string{"--config", path, "--provider", "hub"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	backup, err := os.ReadFile(path + ".legacy.bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != legacy {
		t.Fatal("legacy backup does not match the original config")
	}
	file, err := appconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Current != (appconfig.Selection{Provider: "hub", Model: "legacy-model"}) {
		t.Fatalf("current = %#v", file.Current)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "context_window") || strings.Contains(string(data), "capabilities") {
		t.Fatalf("migrated config contains runtime internals: %s", data)
	}
}

func TestConfigMigrateValidatesBeforeCreatingBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
  "base_url": "https://hub.example/v1",
  "model": "legacy-model",
  "context_window": 131072,
  "model_max_output_tokens": 32768,
  "max_output_tokens": 16384,
  "streaming": true,
  "tools": true,
  "parallel_tool_calls": true,
  "reasoning": false
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runConfigMigrate([]string{"--config", path, "--provider", " "}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("migration unexpectedly accepted an empty provider")
	}
	if _, err := os.Stat(path + ".legacy.bak"); !os.IsNotExist(err) {
		t.Fatalf("invalid migration created a backup: %v", err)
	}
}

func TestREPLModelCatalogDiscoversEveryConfiguredProvider(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer hub-key" {
			t.Errorf("hub authorization = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"hub-new","context_window":200000}]}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer local-key" {
			t.Errorf("local authorization = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"local-new","default_max_output_tokens":4096}]}`)
	}))
	defer second.Close()
	t.Setenv("HUB_KEY", "hub-key")
	t.Setenv("LOCAL_KEY", "local-key")

	path := filepath.Join(t.TempDir(), "config.json")
	file := appconfig.New("hub", first.URL, "hub-current")
	file.Providers["hub"] = appconfig.Provider{BaseURL: first.URL, APIKeyEnv: "HUB_KEY"}
	file.Providers["local"] = appconfig.Provider{BaseURL: second.URL, APIKeyEnv: "LOCAL_KEY"}
	if err := appconfig.Write(path, file, false); err != nil {
		t.Fatal(err)
	}
	document, err := appconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := document.Resolve(appconfig.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	manager := newREPLModelManager(path, current, nil, nil)
	manager.client = modelcatalog.Client{HTTP: first.Client()}
	catalog, err := manager.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"hub:hub-current": false,
		"hub:hub-new":     false,
		"local:local-new": false,
	}
	for _, choice := range catalog.Choices {
		key := choice.Provider + ":" + choice.Model
		if _, ok := want[key]; ok {
			want[key] = true
		}
		if key == "hub:hub-current" && !choice.Current {
			t.Fatalf("current model is not marked: %#v", choice)
		}
		if key == "hub:hub-new" && choice.config.ContextWindow != 200000 {
			t.Fatalf("discovered context window was not applied: %#v", choice.config)
		}
		if key == "local:local-new" && choice.config.MaxOutputTokens != 4096 {
			t.Fatalf("discovered output limit was not applied: %#v", choice.config)
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("catalog missing %s: %#v", key, catalog.Choices)
		}
	}
}
