package coding

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

type dirFS struct{ root string }

func (d dirFS) full(name string) string                    { return filepath.Join(d.root, name) }
func (d dirFS) ReadFile(name string) ([]byte, error)       { return os.ReadFile(d.full(name)) }
func (d dirFS) Stat(name string) (fs.FileInfo, error)      { return os.Stat(d.full(name)) }
func (d dirFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(d.full(name)) }

// ----- 可写能力 -----

func (d dirFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(d.full(name), data, perm)
}

func (d dirFS) MkdirAll(name string, perm fs.FileMode) error {
	return os.MkdirAll(d.full(name), perm)
}

func (d dirFS) Rename(oldName, newName string) error {
	return os.Rename(
		d.full(oldName),
		d.full(newName),
	)
}

func (d dirFS) Remove(name string) error {
	return os.Remove(d.full(name))
}

func executeTool(t *testing.T, tool po.Tool, args any) po.ToolResult {
	t.Helper()
	call, err := po.NewToolCall("call-test", tool.Spec().Name(), args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func toolText(t *testing.T, result po.ToolResult) string {
	t.Helper()
	parts := result.Content()
	if len(parts) != 1 || parts[0].Type != po.ContentText {
		t.Fatalf("unexpected content: %#v", parts)
	}
	return parts[0].Text
}

func TestReadPagesAndPublishesStableVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewToolkit(dirFS{root: dir}).ReadOnlyTools()[0]
	result := executeTool(t, tool, map[string]any{"path": "main.go", "offset": 2, "limit": 2})
	text := toolText(t, result)
	if !strings.Contains(text, "two\nthree") {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(text, "next_offset=4") {
		t.Fatalf("missing continuation hint: %q", text)
	}
	if !strings.Contains(text, "sha256=") {
		t.Fatalf("missing file version: %q", text)
	}
}

func TestReadRejectsWorkspaceEscapeAndBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewToolkit(dirFS{root: dir}).ReadOnlyTools()[0]

	call, _ := po.NewToolCall("call-escape", tool.Spec().Name(), map[string]any{"path": "../secret"})
	if _, err := tool.Execute(context.Background(), call, nil); err == nil {
		t.Fatal("expected workspace escape error")
	}
	call, _ = po.NewToolCall("call-bin", tool.Spec().Name(), map[string]any{"path": "bin.dat"})
	if _, err := tool.Execute(context.Background(), call, nil); err == nil {
		t.Fatal("expected binary file error")
	}
}

func TestLSIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"z.go", "a.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "cmd"), 0o700); err != nil {
		t.Fatal(err)
	}
	tools := NewToolkit(dirFS{root: dir}).ReadOnlyTools()
	text := toolText(t, executeTool(t, tools[3], map[string]any{}))
	if text != "a.go\ncmd/\nz.go" {
		t.Fatalf("ls = %q", text)
	}
}

func TestFindSkipsGeneratedDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pkg/a.go", "pkg/b.txt", "node_modules/hidden.go", ".git/config.go"} {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tools := NewToolkit(dirFS{root: dir}).ReadOnlyTools()
	text := toolText(t, executeTool(t, tools[2], map[string]any{"pattern": "*.go"}))
	if !strings.Contains(text, "pkg/a.go") {
		t.Fatalf("find missing source: %q", text)
	}
	if strings.Contains(text, "node_modules") || strings.Contains(text, ".git") {
		t.Fatalf("find returned ignored path: %q", text)
	}
}

func TestGrepLiteralRegexGlobAndLimit(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.go":    "package demo\nfunc NewAgent() {}\n",
		"b.go":    "package demo\n// NewAgent 说明\n",
		"note.md": "NewAgent is documented\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tools := NewToolkit(dirFS{root: dir}).ReadOnlyTools()
	grep := tools[1]

	text := toolText(t, executeTool(t, grep, map[string]any{"pattern": "newagent", "glob": "*.go"}))
	if !strings.Contains(text, "a.go:2:") || !strings.Contains(text, "b.go:2:") || strings.Contains(text, "note.md") {
		t.Fatalf("literal grep = %q", text)
	}

	text = toolText(t, executeTool(t, grep, map[string]any{"pattern": `func\s+NewAgent`, "regex": true, "case_sensitive": true}))
	if !strings.Contains(text, "a.go:2:") || strings.Contains(text, "b.go") {
		t.Fatalf("regex grep = %q", text)
	}

	text = toolText(t, executeTool(t, grep, map[string]any{"pattern": "package", "limit": 1}))
	if !strings.Contains(text, "stopped after 1 matches") {
		t.Fatalf("limit notice missing: %q", text)
	}
}
