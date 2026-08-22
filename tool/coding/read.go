package coding

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/tool/output"
)

// readArgs 描述模型可控制的读取窗口。offset 使用 1-based 行号，避免模型在
// 人类常见的“第 N 行”语义和 0-based slice index 之间来回换算。
type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// readDetails 是机器可读的 Observation metadata。特别是 SHA256，它会作为
// edit/write 的 optimistic concurrency version。
type readDetails struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	SizeBytes  int    `json:"size_bytes"`
	TotalLines int    `json:"total_lines"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Truncated  bool   `json:"truncated"`
	NextOffset int    `json:"next_offset,omitempty"`
}

// newReadTool 构造一个“分页读取 + 文件版本”的只读 Tool。
//
// 设计重点不是 os.ReadFile，而是让模型能按需扩大观察范围，同时拿到一个稳定
// version token，避免基于陈旧内容直接覆盖文件。
func (t *Toolkit) newReadTool() po.Tool {
	spec := mustSpec(
		"read",
		"Read a UTF-8 text file inside the workspace. Supports 1-indexed offset/limit paging. "+
			"The result includes a SHA-256 file version; preserve it when later editing the file.",
		`{
            "type":"object",
            "properties":{
                "path":{"type":"string","minLength":1},
                "offset":{"type":"integer","minimum":1},
                "limit":{"type":"integer","minimum":1,"maximum":2000}
            },
            "required":["path"],
            "additionalProperties":false
        }`,
	)

	handler := func(ctx context.Context, args readArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return po.ToolResult{}, err
		}
		name, err := normalizeWorkspacePath(args.Path, false)
		if err != nil {
			return po.ToolResult{}, err
		}
		data, err := t.fs.ReadFile(name)
		if err != nil {
			return po.ToolResult{}, fmt.Errorf("read %s: %w", displayPath(name), err)
		}
		if !isTextData(data) {
			return po.ToolResult{}, fmt.Errorf("read %s: file is not UTF-8 text", displayPath(name))
		}

		text := string(data)
		lines := splitLogicalLines(text)
		start := args.Offset
		if start == 0 {
			start = 1
		}
		if len(lines) == 0 {
			start = 1
		}
		if len(lines) > 0 && start > len(lines) {
			return po.ToolResult{}, fmt.Errorf(
				"read %s: offset %d is beyond end of file (%d lines)",
				displayPath(name),
				start,
				len(lines),
			)
		}

		limit := args.Limit
		if limit == 0 {
			limit = defaultReadLimit
		}
		startIndex := start - 1
		endIndex := min(startIndex+limit, len(lines))
		selected := strings.Join(lines[startIndex:endIndex], "\n")
		trunc := output.TruncateHead(selected, output.Limits{})

		// output 层的 byte limit 可能在“请求的行窗口”内部再次截断。这里必须根据
		// 模型实际看见的完整逻辑行重新计算 endLine/nextOffset，不能假设 args.Limit
		// 行都已经可见，否则下一次分页会跳过模型从未真正看到的内容。
		visibleLines := logicalLineCount(trunc.Content)
		endLine := start - 1
		if visibleLines > 0 {
			endLine = start + visibleLines - 1
		}
		hasMore := endLine < len(lines)
		nextOffset := 0
		if hasMore {
			nextOffset = endLine + 1
		}

		digest := fmt.Sprintf("%x", sha256.Sum256(data))
		body := trunc.Content
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += fmt.Sprintf(
			"\n[read: path=%s lines=%d-%d/%d sha256=%s",
			displayPath(name),
			start,
			max(endLine, start-1),
			len(lines),
			digest,
		)
		if hasMore || trunc.Truncated {
			body += fmt.Sprintf(" truncated=true next_offset=%d", nextOffset)
		}
		body += "]"

		return po.NewTextToolResult(body, readDetails{
			Path:       displayPath(name),
			SHA256:     digest,
			SizeBytes:  len(data),
			TotalLines: len(lines),
			StartLine:  start,
			EndLine:    endLine,
			Truncated:  hasMore || trunc.Truncated,
			NextOffset: nextOffset,
		}, false)
	}
	return po.MustNewTypedTool(spec, validateReadArgs, handler)
}

func validateReadArgs(args readArgs) error {
	if _, err := normalizeWorkspacePath(args.Path, false); err != nil {
		return err
	}
	if args.Offset < 0 {
		return fmt.Errorf("offset must be >= 1 when provided")
	}
	if args.Limit < 0 || args.Limit > maxRequestedReadLines {
		return fmt.Errorf("limit must be between 1 and %d when provided", maxRequestedReadLines)
	}
	return nil
}

func splitLogicalLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func logicalLineCount(text string) int {
	return len(splitLogicalLines(text))
}
