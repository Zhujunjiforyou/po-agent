package policy

import (
	"context"

	po "github.com/lemonzjj/po-agent-go"
)

// DenyTools 通过 Tool Name 阻止一组工具。
//
// 它很适合：
//   - 只读运行模式；
//   - 测试环境；
//   - 临时关闭危险 Tool。
//
// 它不理解 Tool Arguments。
// 更细粒度的路径、命令等规则以后使用专门 Policy。
type DenyTools struct {
	name   string
	denied map[string]string
}

// NewDenyTools 创建 Policy，并复制调用方 map。
func NewDenyTools(name string, denied map[string]string) *DenyTools {
	copied := make(map[string]string, len(denied))

	for toolName, reason := range denied {
		copied[toolName] = reason
	}
	return &DenyTools{name: name, denied: copied}
}

func (p *DenyTools) Name() string {
	return p.name
}

func (p *DenyTools) BeforeToolCall(ctx context.Context, input po.BeforeToolCallContext) (po.BeforeToolCallDecision, error) {
	if cause := context.Cause(ctx); cause != nil {
		return po.BeforeToolCallDecision{}, cause
	}

	reason, denied := p.denied[input.Call.Name]
	if !denied {
		return po.BeforeToolCallDecision{}, nil
	}

	return po.BeforeToolCallDecision{
		Block:  true,
		Reason: reason,
	}, nil
}

// 编译期检查接口实现。
var _ BeforeToolPolicy = (*DenyTools)(nil)
