package po_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCoreDoesNotImportOptionalExtensions 固定核心依赖规则：可选包可以依赖 package po，
// 但根核心不能反向依赖运行时策略或具体实现包。
func TestCoreDoesNotImportOptionalExtensions(t *testing.T) {
	modulePath := modulePathFromGoMod(t)
	forbidden := []string{
		modulePath + "/guard",
		modulePath + "/policy",
		modulePath + "/provider",
		modulePath + "/workspace",
		modulePath + "/model/retry",
		modulePath + "/model/scripted",
		modulePath + "/model/timeout",
		modulePath + "/schema/basic",
		modulePath + "/tool/builtin",
		modulePath + "/tool/timeout",
		modulePath + "/tool/output",
		modulePath + "/tool/concurrency",
		modulePath + "/tool/output",
		modulePath + "/session",
		modulePath + "/tool/concurrency",
		modulePath + "/tool/output",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	files := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		parsed, err := parser.ParseFile(files, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if parsed.Name.Name != "po" {
			continue
		}

		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", name, err)
			}
			for _, prefix := range forbidden {
				if path == prefix || strings.HasPrefix(path, prefix+"/") {
					t.Fatalf("core file %s imports optional package %q", name, path)
				}
			}
		}
	}

}

func modulePathFromGoMod(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean("go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "module "))
			if path == "" {
				t.Fatal("go.mod has empty module path")
			}
			return path
		}
	}
	t.Fatal("go.mod does not contain a module directive")
	return ""
}
