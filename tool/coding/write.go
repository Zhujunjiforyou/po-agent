package coding

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
)

type writeArgs struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}

// newWriteTool 负责“创建新文件”或“明确的整文件替换”。
// 已存在文件必须携带 read 返回的 expected_sha256；这阻止模型在不了解当前内容
// 的情况下用一整段生成文本静默覆盖用户/其他进程刚刚做出的修改。
func (t *Toolkit) newWriteTool() po.Tool {
	spec := mustSpec(
		"write",
		"Create a new UTF-8 text file or replace an entire existing file. For an existing "+
			"file you MUST read it first and pass its sha256 as expected_sha256. Prefer edit "+
			"for targeted changes.",
		`{
            "type":"object",
            "properties":{
                "path":{"type":"string","minLength":1},
                "content":{"type":"string"},
                "expected_sha256":{"type":"string"}
            },
            "required":["path","content"],
            "additionalProperties":false
        }`,
	)

	handler := func(ctx context.Context, args writeArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		name, err := normalizeWorkspacePath(args.Path, false)
		if err != nil {
			return po.ToolResult{}, err
		}
		release, err := t.mutations.acquire(ctx, name)
		if err != nil {
			return po.ToolResult{}, err
		}
		defer release()

		var oldDigest string
		perm := fs.FileMode(0o644)
		condition := replaceCondition{requireAbsent: true}
		info, statErr := t.writable.Stat(name)
		switch {
		case statErr == nil:
			if strings.TrimSpace(args.ExpectedSHA256) == "" {
				return po.ToolResult{}, fmt.Errorf(
					"%w: %s; read the file first or use edit",
					ErrVersionRequired,
					displayPath(name),
				)
			}
			data, err := t.writable.ReadFile(name)
			if err != nil {
				return po.ToolResult{}, fmt.Errorf("read %s before write: %w", displayPath(name), err)
			}
			oldDigest = fmt.Sprintf("%x", sha256.Sum256(data))
			if normalizeDigest(args.ExpectedSHA256) != oldDigest {
				return po.ToolResult{}, fmt.Errorf(
					"%w: %s expected %s but current sha256 is %s; read it again before writing",
					ErrFileChanged,
					displayPath(name),
					normalizeDigest(args.ExpectedSHA256),
					oldDigest,
				)
			}
			perm = info.Mode().Perm()
			condition = replaceCondition{expectedSHA256: oldDigest}
		case errors.Is(statErr, fs.ErrNotExist):
			if strings.TrimSpace(args.ExpectedSHA256) != "" {
				return po.ToolResult{}, fmt.Errorf("%w: %s no longer exists", ErrFileChanged, displayPath(name))
			}
			parent := filepath.Dir(name)
			if parent != "." {
				if err := t.writable.MkdirAll(parent, 0o755); err != nil {
					return po.ToolResult{}, fmt.Errorf("create parent directory for %s: %w", displayPath(name), err)
				}
			}
		default:
			return po.ToolResult{}, fmt.Errorf("stat %s: %w", displayPath(name), statErr)
		}

		data := []byte(args.Content)
		if err := atomicReplace(ctx, t.writable, name, data, perm, condition); err != nil {
			return po.ToolResult{}, err
		}
		newDigest := fmt.Sprintf("%x", sha256.Sum256(data))
		action := "Created"
		if oldDigest != "" {
			action = "Replaced"
		}
		body := fmt.Sprintf(
			"%s %s (%d bytes).\nnew_sha256=%s",
			action,
			displayPath(name),
			len(data),
			newDigest,
		)
		return po.NewTextToolResult(body, map[string]any{
			"path":       displayPath(name),
			"old_sha256": oldDigest,
			"new_sha256": newDigest,
			"size_bytes": len(data),
		}, false)
	}
	return po.MustNewTypedTool(spec, nil, handler)
}
