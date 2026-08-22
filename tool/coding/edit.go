package coding

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/tool/output"
)

type replacement struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

type editArgs struct {
	Path           string        `json:"path"`
	ExpectedSHA256 string        `json:"expected_sha256"`
	Edits          []replacement `json:"edits"`
}

// editSpan 是“在原始规范化文本上已经验证过”的修改计划。
// start/end 永远针对同一份 original snapshot，因此同一次 ToolCall 的多个修改不会
// 因前一个 replacement 改变长度而让后一个 offset 漂移。
type editSpan struct {
	start         int
	end           int
	line          int
	oldText       string
	normalizedNew string
}

// newEditTool 实现 read -> versioned edit -> commit 的闭环。
//
// expected_sha256 不是安全加密需求，而是一个 optimistic concurrency token：模型
// 必须证明“我修改的是刚才看到的那个版本”，否则返回冲突并要求重新 read。
func (t *Toolkit) newEditTool() po.Tool {
	spec := mustSpec(
		"edit",
		"Edit one existing UTF-8 text file using exact, unique text replacements. You MUST "+
			"first read the file and pass the sha256 version returned by read as expected_sha256. "+
			"All edits match the original file, not earlier edits in the same call.",
		`{
            "type":"object",
            "properties":{
                "path":{"type":"string","minLength":1},
                "expected_sha256":{"type":"string","minLength":64},
                "edits":{"type":"array","items":{
                    "type":"object",
                    "properties":{
                        "old_text":{"type":"string","minLength":1},
                        "new_text":{"type":"string"}
                    },
                    "required":["old_text","new_text"],
                    "additionalProperties":false
                }}
            },
            "required":["path","expected_sha256","edits"],
            "additionalProperties":false
        }`,
	)

	handler := func(ctx context.Context, args editArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
		name, err := normalizeWorkspacePath(args.Path, false)
		if err != nil {
			return po.ToolResult{}, err
		}
		release, err := t.mutations.acquire(ctx, name)
		if err != nil {
			return po.ToolResult{}, err
		}
		defer release()

		data, err := t.writable.ReadFile(name)
		if err != nil {
			return po.ToolResult{}, fmt.Errorf("edit %s: %w", displayPath(name), err)
		}
		if !isTextData(data) {
			return po.ToolResult{}, fmt.Errorf("edit %s: file is not UTF-8 text", displayPath(name))
		}
		oldDigest := fmt.Sprintf("%x", sha256.Sum256(data))
		if normalizeDigest(args.ExpectedSHA256) != oldDigest {
			return po.ToolResult{}, fmt.Errorf(
				"%w: %s expected %s but current sha256 is %s; read the file again before editing",
				ErrFileChanged,
				displayPath(name),
				normalizeDigest(args.ExpectedSHA256),
				oldDigest,
			)
		}

		info, err := t.writable.Stat(name)
		if err != nil {
			return po.ToolResult{}, fmt.Errorf("stat %s: %w", displayPath(name), err)
		}
		originalText := string(data)
		bom, textWithoutBOM := splitBOM(originalText)
		lineEnding := detectLineEnding(textWithoutBOM)
		normalized := normalizeLF(textWithoutBOM)
		spans, err := planEdits(normalized, args.Edits, displayPath(name))
		if err != nil {
			return po.ToolResult{}, err
		}
		newNormalized := applyPlannedEdits(normalized, spans)
		newText := bom + restoreLineEnding(newNormalized, lineEnding)

		condition := replaceCondition{expectedSHA256: oldDigest}
		if err := atomicReplace(ctx, t.writable, name, []byte(newText), info.Mode().Perm(), condition); err != nil {
			return po.ToolResult{}, err
		}
		newDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(newText)))
		diff := renderReplacementDiff(displayPath(name), spans)
		trunc := output.TruncateHead(diff, output.Limits{MaxBytes: 24 * 1024, MaxLines: 800})
		body := fmt.Sprintf(
			"Edited %s with %d replacement(s).\nold_sha256=%s\nnew_sha256=%s",
			displayPath(name),
			len(spans),
			oldDigest,
			newDigest,
		)
		if trunc.Content != "" {
			body += "\n\n" + trunc.Content
		}
		if trunc.Truncated {
			body += "\n\n[diff truncated]"
		}
		return po.NewTextToolResult(body, map[string]any{
			"path":           displayPath(name),
			"old_sha256":     oldDigest,
			"new_sha256":     newDigest,
			"replacements":   len(spans),
			"diff":           trunc.Content,
			"diff_truncated": trunc.Truncated,
		}, false)
	}
	return po.MustNewTypedTool(spec, validateEditArgs, handler)
}

func validateEditArgs(args editArgs) error {
	if _, err := normalizeWorkspacePath(args.Path, false); err != nil {
		return err
	}
	if len(normalizeDigest(args.ExpectedSHA256)) != 64 {
		return fmt.Errorf("expected_sha256 must be a 64-character SHA-256 digest from read")
	}
	if len(args.Edits) == 0 {
		return fmt.Errorf("edits must contain at least one replacement")
	}
	for i, edit := range args.Edits {
		if edit.OldText == "" {
			return fmt.Errorf("edits[%d].old_text cannot be empty", i)
		}
	}
	return nil
}

// planEdits 先在同一份 original snapshot 上验证全部 replacement，再统一应用。
// old_text 必须唯一、不同 replacement 不允许重叠。这样一个 ToolCall 要么形成一份
// 清楚的修改计划，要么在真正写文件之前失败。
func planEdits(original string, edits []replacement, path string) ([]editSpan, error) {
	spans := make([]editSpan, 0, len(edits))
	for i, edit := range edits {
		oldText := normalizeLF(edit.OldText)
		newText := normalizeLF(edit.NewText)
		count := strings.Count(original, oldText)
		if count == 0 {
			return nil, fmt.Errorf(
				"edit %s: edits[%d].old_text was not found; read the file again and use exact current text",
				path,
				i,
			)
		}
		if count > 1 {
			return nil, fmt.Errorf(
				"edit %s: edits[%d].old_text matches %d locations; include more surrounding text so the replacement is unique",
				path,
				i,
				count,
			)
		}
		start := strings.Index(original, oldText)
		spans = append(spans, editSpan{
			start:         start,
			end:           start + len(oldText),
			line:          1 + strings.Count(original[:start], "\n"),
			oldText:       oldText,
			normalizedNew: newText,
		})
	}

	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return nil, fmt.Errorf(
				"edit %s: replacements overlap; merge nearby changes into one old_text/new_text block",
				path,
			)
		}
	}
	return spans, nil
}

func applyPlannedEdits(original string, spans []editSpan) string {
	var b strings.Builder
	last := 0
	for _, span := range spans {
		b.WriteString(original[last:span.start])
		b.WriteString(span.normalizedNew)
		last = span.end
	}
	b.WriteString(original[last:])
	return b.String()
}

func splitBOM(text string) (bom, rest string) {
	if strings.HasPrefix(text, "\ufeff") {
		return "\ufeff", strings.TrimPrefix(text, "\ufeff")
	}
	return "", text
}

func detectLineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func normalizeLF(text string) string {
	return strings.ReplaceAll(text, "\r\n", "\n")
}

func restoreLineEnding(text, lineEnding string) string {
	if lineEnding == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// renderReplacementDiff 只生成“给模型/用户看的修改观察”，它不是可以重新 apply 的
// 标准 unified patch。真正的 mutation intent 是上面的 exact replacement list；Diff
// 只是 commit 后用于检查结果的展示层。
func renderReplacementDiff(path string, spans []editSpan) string {
	var b strings.Builder
	b.WriteString("--- a/")
	b.WriteString(path)
	b.WriteString("\n+++ b/")
	b.WriteString(path)
	b.WriteByte('\n')
	for _, span := range spans {
		fmt.Fprintf(&b, "@@ around line %d @@\n", span.line)
		for _, line := range strings.Split(span.oldText, "\n") {
			b.WriteByte('-')
			b.WriteString(line)
			b.WriteByte('\n')
		}
		for _, line := range strings.Split(span.normalizedNew, "\n") {
			b.WriteByte('+')
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}
