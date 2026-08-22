package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstructionsUsesPrecedenceAndSpecificityOrder(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "parent", "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "global.md")
	parent := filepath.Join(root, "parent", "AGENTS.md")
	override := filepath.Join(workspace, "AGENTS.override.md")
	shadowed := filepath.Join(workspace, "AGENTS.md")
	writeInstruction(t, global, "global")
	writeInstruction(t, parent, "parent")
	writeInstruction(t, override, "override")
	writeInstruction(t, shadowed, "shadowed")

	documents, err := LoadInstructions(InstructionOptions{
		WorkspaceRoot: workspace,
		GlobalFile:    global,
		MaxTotalBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	positions := map[string]int{}
	for index, document := range documents {
		positions[document.Path] = index
	}
	for _, path := range []string{global, parent, override} {
		if _, ok := positions[path]; !ok {
			t.Fatalf("instructions do not contain %s: %#v", path, documents)
		}
	}
	if positions[global] >= positions[parent] || positions[parent] >= positions[override] {
		t.Fatalf("instruction order = %#v", documents)
	}
	if _, ok := positions[shadowed]; ok {
		t.Fatalf("shadowed AGENTS.md was loaded: %#v", documents)
	}
}

func TestLoadInstructionsHonorsFileAndTotalLimits(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	writeInstruction(t, path, "0123456789")

	documents, err := LoadInstructions(InstructionOptions{
		WorkspaceRoot: root,
		MaxFileBytes:  4,
		MaxTotalBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].Content != "0123" || !documents[0].Truncated {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestReadInstructionTruncatesAtUTF8Boundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	writeInstruction(t, path, "ab你好")
	document, err := readInstruction(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "ab" || !document.Truncated {
		t.Fatalf("document = %#v", document)
	}
}

func TestReadInstructionRejectsInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readInstruction(path, 64); err == nil {
		t.Fatal("readInstruction() accepted invalid UTF-8")
	}
}

func writeInstruction(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
