package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stdout.String() != "po dev\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunVersionUsesBuildInjectedValue(t *testing.T) {
	original := version
	version = "v0.1.0"
	t.Cleanup(func() { version = original })

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stdout.String() != "po v0.1.0\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestConfigInitDoesNotPersistAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"config", "init",
		"--config", path,
		"--base-url", "https://hub.example/v1",
		"--model", "Qwen/Qwen3.6-27B",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ToLower(data), []byte("api_key\":")) {
		t.Fatalf("config unexpectedly persisted a key field: %s", data)
	}
	if !bytes.Contains(data, []byte(`"api_key_env": "PO_API_KEY"`)) {
		t.Fatalf("config = %s", data)
	}
}

func TestRunPromptAgainstOpenAICompatibleServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEData(w, `{"id":"cli-1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`)
		_, _ = io.WriteString(w, "data: {\"id\":\"cli-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	config := qwen36_27BConfig(server.URL+"/v1", "test-model")
	if err := writeConfig(path, config, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PO_API_KEY", "test-key")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", path, "--no-tools", "-p", "say hi"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != "hello\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestDoctorChecksStructuredToolCalling(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requestNumber == 1 {
			writeSSEData(
				w,
				`{"id":"doctor-text","choices":[{"index":0,"delta":{"content":"PO_OK"},"finish_reason":null}]}`,
			)
			_, _ = io.WriteString(w, "data: {\"id\":\"doctor-text\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		} else {
			if _, ok := body["tools"]; !ok {
				t.Fatal("doctor tool probe did not send tools")
			}
			writeSSEData(w, `{"id":"doctor-tool","choices":[{"index":0,"delta":{"tool_calls":[`+
				`{"index":0,"id":"call-doc","function":{"name":"calculator",`+
				`"arguments":"{\"operation\":\"multiply\",\"a\":17,\"b\":19}"}}]},`+
				`"finish_reason":null}]}`)
			_, _ = io.WriteString(w, "data: {\"id\":\"doctor-tool\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeConfig(path, qwen36_27BConfig(server.URL+"/v1", "test-model"), false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PO_API_KEY", "test-key")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"doctor", "--config", path}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "text: ok") || !strings.Contains(stdout.String(), "tools: ok (calculator)") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunPromptCanReadWorkspace(t *testing.T) {
	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "README.md"), []byte("# Po Workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requestNumber == 1 {
			tools, ok := body["tools"].([]any)
			if !ok || len(tools) == 0 {
				t.Fatalf("first request tools = %#v", body["tools"])
			}
			writeSSEData(w, `{"id":"read-1","choices":[{"index":0,"delta":{"tool_calls":[`+
				`{"index":0,"id":"call-read","function":{"name":"read",`+
				`"arguments":"{\"path\":\"README.md\"}"}}]},"finish_reason":null}]}`)
			_, _ = io.WriteString(w, "data: {\"id\":\"read-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			messages, ok := body["messages"].([]any)
			if !ok {
				t.Fatalf("messages = %#v", body["messages"])
			}
			encoded, _ := json.Marshal(messages)
			if !bytes.Contains(encoded, []byte("Po Workspace")) {
				t.Fatalf("tool result missing workspace content: %s", encoded)
			}
			writeSSEData(w, `{"id":"read-2","choices":[{"index":0,"delta":{"content":"workspace ok"},"finish_reason":null}]}`)
			_, _ = io.WriteString(w, "data: {\"id\":\"read-2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeConfig(path, qwen36_27BConfig(server.URL+"/v1", "test-model"), false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PO_API_KEY", "test-key")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := []string{"--config", path, "--workspace", workspaceDir, "-p", "read the README"}
	code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.String() != "workspace ok\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestWriteToolsRequireExplicitAllowWrite(t *testing.T) {
	workspaceDir := t.TempDir()
	var requestNumber atomic.Int32
	var firstHasEdit atomic.Bool
	var secondHasEdit atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestNumber.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		hasEdit := false
		if rawTools, ok := body["tools"].([]any); ok {
			for _, raw := range rawTools {
				tool, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				fn, _ := tool["function"].(map[string]any)
				if fn["name"] == "edit" {
					hasEdit = true
				}
			}
		}
		if n == 1 {
			firstHasEdit.Store(hasEdit)
		} else if n == 2 {
			secondHasEdit.Store(hasEdit)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"done\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"done\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeConfig(path, qwen36_27BConfig(server.URL+"/v1", "test-model"), false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PO_API_KEY", "test-key")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := []string{"--config", path, "--workspace", workspaceDir, "-p", "inspect"}
	code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("read-only run code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	args = []string{"--config", path, "--workspace", workspaceDir, "--allow-write", "-p", "edit"}
	code = run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("write-enabled run code=%d stderr=%q", code, stderr.String())
	}
	if firstHasEdit.Load() {
		t.Fatal("edit tool was advertised without --allow-write")
	}
	if !secondHasEdit.Load() {
		t.Fatal("edit tool was not advertised with --allow-write")
	}
}

func TestRunAllowWriteCanCreateWorkspaceFile(t *testing.T) {
	workspaceDir := t.TempDir()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requestNumber == 1 {
			writeSSEData(w, `{"id":"write-1","choices":[{"index":0,"delta":{"tool_calls":[`+
				`{"index":0,"id":"call-write","function":{"name":"write",`+
				`"arguments":"{\"path\":\"notes/new.txt\",\"content\":\"created by po\\n\"}"}}]},`+
				`"finish_reason":null}]}`)
			_, _ = io.WriteString(w, "data: {\"id\":\"write-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			messages, ok := body["messages"].([]any)
			if !ok {
				t.Fatalf("messages = %#v", body["messages"])
			}
			encoded, _ := json.Marshal(messages)
			if !bytes.Contains(encoded, []byte("Created notes/new.txt")) {
				t.Fatalf("write result missing from second request: %s", encoded)
			}
			writeSSEData(w, `{"id":"write-2","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":null}]}`)
			_, _ = io.WriteString(w, "data: {\"id\":\"write-2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeConfig(path, qwen36_27BConfig(server.URL+"/v1", "test-model"), false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PO_API_KEY", "test-key")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := []string{
		"--config", path,
		"--workspace", workspaceDir,
		"--allow-write",
		"--yes",
		"-p", "create notes/new.txt",
	}
	code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(workspaceDir, "notes", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "created by po\n" {
		t.Fatalf("file = %q", data)
	}
}

func writeSSEData(w io.Writer, data string) {
	_, _ = io.WriteString(w, "data: "+data+"\n\n")
}
