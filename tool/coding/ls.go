package coding

import (
	"context"
	"fmt"
	"strings"

	"github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/tool/output"
)

type lsArgs struct {
	Path  string `json:"path,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

func (t *Toolkit) newLSTool() po.Tool {
	spec := mustSpec(
		"ls",
		`List one workspace directory in deterministic name order. 
		Directories end with '/'. Use find for recursive filename discovery.`,
		`{
            "type":"object",
            "properties":{
                "path":{"type":"string"},
                "limit":{"type":"integer","minimum":1,"maximum":200}
            },
            "additionalProperties":false
        }`,
	)
	// T = lsArgs
	return po.MustNewTypedTool(spec, nil, func(ctx context.Context, args lsArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return po.ToolResult{}, err
		}
		name, err := normalizeWorkspacePath(args.Path, true)
		if err != nil {
			return po.ToolResult{}, fmt.Errorf("ls %s: %w", displayPath(name), err)
		}
		entries, err := t.fs.ReadDir(name)
		if err != nil {
			return po.ToolResult{}, fmt.Errorf("ls %s: %w", displayPath(name), err)
		}
		entries = sortedEntries(entries)
		limit := clampResultLimit(args.Limit)
		shown := entries
		if len(shown) > limit {
			shown = shown[:limit]
		}

		var b strings.Builder
		for _, entry := range shown {
			b.WriteString(entry.Name())
			if entry.IsDir() {
				b.WriteByte('/')
			}
			b.WriteByte('\n')
		}
		text := strings.TrimSuffix(b.String(), "\n")
		trunc := output.TruncateHead(text, output.Limits{})
		body := trunc.Content
		if len(entries) > len(shown) || trunc.Truncated {
			body += fmt.Sprintf("\n\n[ls truncated: showing %d of %d entires]", len(shown), len(entries))
		}
		return po.NewTextToolResult(body, map[string]any{
			"path":      displayPath(name),
			"entries":   len(entries),
			"shown":     len(shown),
			"truncated": len(entries) > len(shown) || trunc.Truncated,
		}, false)
	})
}
