// Package coding 提供 Po Coding Agent 使用的 Workspace 文件工具。
//
// 这里故意只依赖一个很小的文件系统 capability，而不是直接依赖宿主绝对路径。
// 生产环境由 workspace.Workspace（Go 1.25+ os.Root）实现这个接口；测试则可以
// 使用临时目录实现，从而把“Agent Tool 语义”和“具体 OS 边界”解耦。
package coding

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	defaultReadLimit      = 400
	maxRequestedReadLines = 2000
	defaultResultLimit    = 100
	maxResultLimit        = 200
	maxSearchFileBytes    = 2 * 1024 * 1024
	maxSearchLineBytes    = 4 * 1024
)

// FileSystem 是只读 Coding Tool 所需要的最小 capability。
//
// 所有 path 都必须是 workspace-relative。这个接口本身不声称能阻止路径逃逸；
// 生产实现必须兑现该安全语义，Po 的 workspace.Workspace 使用 os.Root 完成。
type FileSystem interface {
	ReadFile(name string) ([]byte, error)
	Stat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
}

// WritableFileSystem 在只读capability之上增加最小修改原语
type WritableFileSystem interface {
	FileSystem
	WriteFile(name string, data []byte, perm fs.FileMode) error
	MkdirAll(name string, perm fs.FileMode) error
	Rename(oldName, newName string) error
	Remove(name string) error
}

func normalizeWorkspacePath(raw string, allowRoot bool) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		if allowRoot {
			return ".", nil
		}
		return "", fmt.Errorf("path is required")
	}

	cleaned := filepath.Clean(value)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path must be relative to the workspace: %q", raw)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the workspace: %q", raw)
	}
	if cleaned == "." && !allowRoot {
		return "", fmt.Errorf("path must name a file")
	}
	return cleaned, nil
}

func displayPath(name string) string {
	if name == "." {
		return "."
	}
	return filepath.ToSlash(name)
}

func joinWorkspacePath(parent, child string) string {
	if parent == "." {
		return child
	}
	return filepath.Join(parent, child)
}

func sortedEntries(entries []fs.DirEntry) []fs.DirEntry {
	out := append([]fs.DirEntry(nil), entries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func isIgnoredDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", ".venv", "venv", "dist", "build", "target", "coverage", ".next", ".cache":
		return true
	default:
		return false
	}
}

func isTextData(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	// NUL 是源码工具中一个便宜但实用的二进制信号。这里不追求完整 MIME 检测，
	// 目标只是避免把 object/executable 等任意字节误当成模型可读文本。
	return !strings.ContainsRune(string(data), '\x00')
}

func clampResultLimit(value int) int {
	if value <= 0 {
		return defaultResultLimit
	}
	if value > maxResultLimit {
		return maxResultLimit
	}
	return value
}

func truncateSearchLine(line string) string {
	if len(line) <= maxSearchLineBytes {
		return line
	}
	end := maxSearchLineBytes
	for end > 0 && !utf8.ValidString(line[:end]) {
		end--
	}
	return line[:end] + " …[line truncated]"
}
