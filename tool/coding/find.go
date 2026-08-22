package coding

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/tool/output"
)

type findArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// newFindTool 用文件名/相对路径做递归发现。它故意只做“找到候选文件”，
// 不读取文件内容；这样模型可以先缩小搜索空间，再用 grep/read 获取证据。
func (t *Toolkit) newFindTool() po.Tool {
	spec := mustSpec(
		"find",
		`Find workspace files by shell-style glob. Search is recursive, deterministic, 
		skips common generated/vendor directories, and returns workspace-relative paths.`,
		`{
            "type":"object",
            "properties":{
                "pattern":{"type":"string","minLength":1},
                "path":{"type":"string"},
                "limit":{"type":"integer","minimum":1,"maximum":200}
            },
            "required":["pattern"],
            "additionalProperties":false
        }`,
	)

	return po.MustNewTypedTool(spec, nil, func(ctx context.Context, args findArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		root, err := normalizeWorkspacePath(args.Path, true)
		if err != nil {
			return po.ToolResult{}, err
		}
		pattern := strings.TrimSpace(args.Pattern)
		if _, err := path.Match(patternForCheck(pattern), "probe.go"); err != nil {
			return po.ToolResult{}, fmt.Errorf("invalid find glob %q: %w", pattern, err)
		}
		limit := clampResultLimit(args.Limit)
		matches := make([]string, 0, min(limit, 32))
		seen := 0
		err = walkFiles(ctx, t.fs, root, func(name string, _ fs.FileInfo) (bool, error) {
			seen++
			if seen%200 == 0 {
				if err := po.EmitToolUpdate(ctx, emit, po.NewToolUpdate(fmt.Sprintf("find scanned %d files", seen))); err != nil {
					return false, err
				}
			}
			rel := displayPath(name)
			ok, err := matchFindGlob(pattern, rel)
			if err != nil {
				return false, err
			}
			if ok {
				matches = append(matches, rel)
			}
			return len(matches) >= limit, nil
		})
		if err != nil {
			return po.ToolResult{}, fmt.Errorf("find in %s: %w", displayPath(root), err)
		}
		text := strings.Join(matches, "\n")
		trunc := output.TruncateHead(text, output.Limits{})
		body := trunc.Content
		limitReached := len(matches) >= limit
		if limitReached || trunc.Truncated {
			body += fmt.Sprintf("\n\n[find stopped after %d matches; narrow path/pattern for more precise results]", len(matches))
		}
		if body == "" {
			body = "No matching files."
		}
		return po.NewTextToolResult(body, map[string]any{
			"path":          displayPath(root),
			"pattern":       pattern,
			"matches":       len(matches),
			"files_scanned": seen,
			"limit_reached": limitReached,
		}, false)
	})
}

func patternForCheck(pattern string) string {
	if strings.HasPrefix(pattern, "**/") {
		return strings.TrimPrefix(pattern, "**/")
	}
	return pattern
}

// matchFindGlob 是一个轻量纯 Go glob 实现，不复制 fd/ripgrep 的完整 glob 语义。
// `**/foo.go` 按 basename 匹配，避免依赖平台相关的 glob 扩展。
func matchFindGlob(pattern, rel string) (bool, error) {
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	if strings.HasPrefix(pattern, "**/") {
		pattern = strings.TrimPrefix(pattern, "**/")
		return path.Match(pattern, path.Base(rel))
	}
	if strings.Contains(pattern, "/") {
		return path.Match(pattern, rel)
	}
	return path.Match(pattern, path.Base(rel))
}
