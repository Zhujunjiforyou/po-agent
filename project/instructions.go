package project

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	DefaultMaxInstructionFileBytes = 64 * 1024
	DefaultMaxInstructionsBytes    = 256 * 1024
)

// InstructionFile 是从磁盘加载的一份项目说明。
type InstructionFile struct {
	Path      string
	Content   string
	Truncated bool
}

// InstructionOptions 控制项目说明的来源与大小边界。
type InstructionOptions struct {
	WorkspaceRoot string
	GlobalFile    string
	MaxFileBytes  int
	MaxTotalBytes int
}

// LoadInstructions 按从宽到窄的顺序加载说明：global -> workspace ancestors -> workspace。
// 每个目录依次选择 AGENTS.override.md、AGENTS.md 或 CLAUDE.md 中的第一份普通文件。
func LoadInstructions(opts InstructionOptions) ([]InstructionFile, error) {
	root, err := filepath.Abs(opts.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	maxFile := opts.MaxFileBytes
	if maxFile <= 0 {
		maxFile = DefaultMaxInstructionFileBytes
	}
	maxTotal := opts.MaxTotalBytes
	if maxTotal <= 0 {
		maxTotal = DefaultMaxInstructionsBytes
	}

	candidates := []string{}
	if strings.TrimSpace(opts.GlobalFile) != "" {
		if info, err := os.Stat(opts.GlobalFile); err == nil && info.Mode().IsRegular() {
			candidates = append(candidates, opts.GlobalFile)
		}
	}
	for _, dir := range ancestorDirs(root) {
		if file := chooseInstructionFile(dir); file != "" {
			candidates = append(candidates, file)
		}
	}

	out := make([]InstructionFile, 0, len(candidates))
	used := 0
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		remain := maxTotal - used
		if remain <= 0 {
			break
		}
		limit := maxFile
		if remain < limit {
			limit = remain
		}
		doc, err := readInstruction(abs, limit)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		used += len(doc.Content)
		out = append(out, doc)
	}
	return out, nil
}

func ancestorDirs(root string) []string {
	chain := []string{}
	current := filepath.Clean(root)
	for {
		chain = append(chain, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func chooseInstructionFile(dir string) string {
	for _, name := range []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

func readInstruction(path string, maxBytes int) (InstructionFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return InstructionFile{}, fmt.Errorf("open project instructions %q: %w", path, err)
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, int64(maxBytes+utf8.UTFMax)))
	data, err := io.ReadAll(reader)
	if err != nil {
		return InstructionFile{}, fmt.Errorf("read project instructions %q: %w", path, err)
	}
	truncated := len(data) > maxBytes
	if truncated {
		data, err = truncateUTF8(data, maxBytes)
		if err != nil {
			return InstructionFile{}, fmt.Errorf("read project instructions %q: %w", path, err)
		}
	} else if !utf8.Valid(data) {
		return InstructionFile{}, fmt.Errorf("read project instructions %q: content is not valid UTF-8", path)
	}
	return InstructionFile{Path: path, Content: string(data), Truncated: truncated}, nil
}

func truncateUTF8(data []byte, maxBytes int) ([]byte, error) {
	prefix := data[:maxBytes]
	if utf8.Valid(prefix) {
		return prefix, nil
	}

	minimum := max(0, maxBytes-utf8.UTFMax+1)
	for start := maxBytes - 1; start >= minimum; start-- {
		if !utf8.Valid(prefix[:start]) {
			continue
		}
		r, size := utf8.DecodeRune(data[start:])
		if r != utf8.RuneError || size > 1 {
			return prefix[:start], nil
		}
	}
	return nil, fmt.Errorf("content is not valid UTF-8")
}
