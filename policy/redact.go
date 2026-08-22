package policy

import (
	"context"
	"fmt"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
)

// RedactText 对 ToolResult 中已经明确知道的 Secret 字符串做替换。
//
// 这只是 Result-level 的一层防护，
// 不是完整 DLP，也不能替代最小权限。
type RedactText struct {
	name        string
	secrets     []string
	replacement string
}

func NewRedactText(name string, secrets []string) *RedactText {
	return &RedactText{
		name: name,
		// 复制 slice，防止调用者后续修改运行中的 Policy。
		secrets:     append([]string(nil), secrets...),
		replacement: "[REDACTED]",
	}
}

func (p *RedactText) Name() string {
	return p.name
}

func (p *RedactText) AfterToolCall(ctx context.Context, input po.AfterToolCallContext) (po.ToolResult, bool, error) {
	if cause := context.Cause(ctx); cause != nil {
		return po.ToolResult{}, false, cause
	}

	parts := input.Result.Content()
	for index := range parts {
		for _, secret := range p.secrets {
			if secret == "" {
				continue
			}
			parts[index].Text = strings.ReplaceAll(parts[index].Text, secret, p.replacement)
		}
	}

	// Details 默认不会直接进入模型，但它可能进入 Session / UI / Debug，
	// 所以简单已知 Secret 同样替换。
	detailText := string(input.Result.Details())
	for _, secret := range p.secrets {
		if secret == "" {
			continue
		}
		detailText = strings.ReplaceAll(detailText, secret, p.replacement)
	}

	result, err := po.NewToolResult(parts, []byte(detailText), input.Result.Terminate())
	if err != nil {
		return po.ToolResult{}, false, fmt.Errorf("rebuild redacted result: %w", err)
	}

	// Redaction 不改变原 Tool Result 的错误语义。
	return result, input.IsError, nil
}

var _ AfterToolPolicy = (*RedactText)(nil)
