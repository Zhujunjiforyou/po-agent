package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceReadWriteInsideRoot(t *testing.T) {
	dir := t.TempDir()
	workspace, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if err := workspace.WriteFile("hello.txt", []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := workspace.ReadFile("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q", data)
	}
}

func TestWorkspaceBlocksParentTraversal(t *testing.T) {
	parent := t.TempDir()
	rootDir := filepath.Join(parent, "project")
	if err := os.Mkdir(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := Open(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	_, err = workspace.ReadFile("../secret.txt")
	if err == nil {
		t.Fatal("expected parent traversal to be rejected")
	}
}

func TestWorkspaceBlocksSymlinkEscape(t *testing.T) {
	rootDir := t.TempDir()
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(rootDir, "secret-link")
	if err := os.Symlink(secret, link); err != nil {
		// Windows / 某些 CI 环境可能没有创建 symlink 权限。
		t.Skipf("symlink unavailable: %v", err)
	}
	workspace, err := Open(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	_, err = workspace.ReadFile("secret-link")
	if err == nil {
		t.Fatal("expected escaping symlink to be rejected")
	}
}

func TestWorkspaceCannotBeUsedAfterClose(t *testing.T) {
	workspace, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = workspace.ReadFile("anything")
	if err == nil {
		t.Fatal("expected closed workspace error")
	}
}
