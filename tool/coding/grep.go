package coding

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/tool/output"
)

// grepArgs 把“搜索什么”“在哪里搜索”“如何解释 pattern”显式分开。
// 当前 Po 默认使用 case-insensitive literal，模型只有在确实需要时才打开 regex，
// 这是一个偏向可预测性的 Go-native 取舍，与当前 Pi 的默认行为并不完全相同。
type grepArgs struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path,omitempty"`
	Glob          string `json:"glob,omitempty"`
	Regex         bool   `json:"regex,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type lineMatcher func(string) bool

// newGrepTool 构造有界递归文本搜索。
//
// 这一实现故意不用 subprocess/ripgrep，以保持 Tool 语义、Workspace 边界和
// bounded observation 清晰；它目前不会自动读取 .gitignore，需要时可以接入
// rg backend 改善搜索能力。
func (t *Toolkit) newGrepTool() po.Tool {
	spec := mustSpec(
		"grep",
		`Search UTF-8 text files recursively inside the workspace. By default pattern is a case-insensitive literal; 
		set regex=true for Go regular expressions. Optional glob filters file names such as '*.go'.`,
		`{
            "type":"object",
            "properties":{
                "pattern":{"type":"string","minLength":1},
                "path":{"type":"string"},
                "glob":{"type":"string"},
                "regex":{"type":"boolean"},
                "case_sensitive":{"type":"boolean"},
                "limit":{"type":"integer","minimum":1,"maximum":200}
            },
            "required":["pattern"],
            "additionalProperties":false
        }`,
	)

	return po.MustNewTypedTool(spec, nil, func(ctx context.Context, args grepArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		root, err := normalizeWorkspacePath(args.Path, true)
		if err != nil {
			return po.ToolResult{}, err
		}
		matcher, err := buildLineMatcher(args)
		if err != nil {
			return po.ToolResult{}, err
		}
		limit := clampResultLimit(args.Limit)
		matches := make([]string, 0, min(limit, 32))
		filesScanned := 0
		filesSkipped := 0
		err = walkFiles(ctx, t.fs, root, func(name string, info fs.FileInfo) (bool, error) {
			if args.Glob != "" {
				ok, err := matchFindGlob(args.Glob, displayPath(name))
				if err != nil {
					return false, fmt.Errorf("invalid grep glob %q: %w", args.Glob, err)
				}
				if !ok {
					return false, nil
				}
			}
			if info.Size() > maxSearchFileBytes {
				filesSkipped++
				return false, nil
			}
			data, err := t.fs.ReadFile(name)
			if err != nil {
				return false, fmt.Errorf("read %s: %w", displayPath(name), err)
			}
			if !isTextData(data) {
				filesSkipped++
				return false, nil
			}
			filesScanned++
			if filesScanned%100 == 0 {
				if err := po.EmitToolUpdate(ctx, emit, po.NewToolUpdate(fmt.Sprintf("grep searched %d text files", filesScanned))); err != nil {
					return false, err
				}
			}
			for lineIndex, line := range splitLogicalLines(string(data)) {
				if matcher(line) {
					matches = append(matches, fmt.Sprintf("%s:%d:%s", displayPath(name), lineIndex+1, truncateSearchLine(line)))
					if len(matches) >= limit {
						return true, nil
					}
				}
			}
			return false, nil
		})
		if err != nil {
			return po.ToolResult{}, fmt.Errorf("grep in %s: %w", displayPath(root), err)
		}

		text := strings.Join(matches, "\n")
		trunc := output.TruncateHead(text, output.Limits{})
		body := trunc.Content
		limitReached := len(matches) >= limit
		if limitReached || trunc.Truncated {
			body += fmt.Sprintf("\n\n[grep stopped after %d matches; narrow path/glob/pattern to continue]", len(matches))
		}
		if body == "" {
			body = "No matches."
		}
		return po.NewTextToolResult(body, map[string]any{
			"path":          displayPath(root),
			"pattern":       args.Pattern,
			"matches":       len(matches),
			"files_scanned": filesScanned,
			"files_skipped": filesSkipped,
			"limit_reached": limitReached,
		}, false)
	})
}

// buildLineMatcher 把“literal/regex + case sensitivity”转换成一个稳定 matcher。
// Tool 执行循环因此只关心逐行匹配，而不需要重复分支。
func buildLineMatcher(args grepArgs) (lineMatcher, error) {
	pattern := args.Pattern
	if args.Regex {
		if !args.CaseSensitive {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex %q: %w", args.Pattern, err)
		}
		return re.MatchString, nil
	}
	if args.CaseSensitive {
		return func(line string) bool { return strings.Contains(line, pattern) }, nil
	}
	needle := strings.ToLower(pattern)
	return func(line string) bool { return strings.Contains(strings.ToLower(line), needle) }, nil
}
