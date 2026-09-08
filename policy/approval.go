package policy

import (
	"context"
	"fmt"

	po "github.com/lemonzjj/po-agent-go"
)

// ApprovalRequest 是 Runtime 交给产品 UI 的审批请求。
//
// Policy 只说明：
//   - 哪个 Tool；
//   - 为什么需要批准；
//   - 完整 Tool Call 是什么。
//
// UI 自己决定怎样展示。
type ApprovalRequest struct {
	ToolName string
	Reason   string
	Call     po.ToolCall
}

// Approver 是 Policy 与具体 UI 之间的边界。
//
// CLI 可以实现终端确认。
// Web 可以实现对话框。
// 测试可以实现固定 true / false。
type Approver interface {
	Approve(ctx context.Context, request ApprovalRequest) (bool, error)
}

// Approval 在指定 Tool 被调用时请求人工批准。
//
// 当前按 Tool Name 判断。参数级风险分类应由独立 Policy 实现。
type Approval struct {
	name     string
	risky    map[string]string
	approver Approver
}

func NewApproval(name string, risky map[string]string, approver Approver) *Approval {
	copied := make(map[string]string, len(risky))
	for toolName, reason := range risky {
		copied[toolName] = reason
	}
	return &Approval{name: name, risky: copied, approver: approver}
}

func (p *Approval) Name() string {
	return p.name
}

func (p *Approval) BeforeToolCall(ctx context.Context, input po.BeforeToolCallContext) (po.BeforeToolCallDecision, error) {
	reason, needsApproval := p.risky[input.Call.Name]
	if !needsApproval {
		return po.BeforeToolCallDecision{}, nil
	}

	// 没有交互式 Approver 时：
	// 需要批准的操作默认不执行。
	if p.approver == nil {
		return po.BeforeToolCallDecision{Block: true, Reason: "approval is required but no approver is available"}, nil
	}
	allowed, err := p.approver.Approve(ctx, ApprovalRequest{
		ToolName: input.Call.Name,
		Reason:   reason,
		Call:     input.Call,
	})
	if err != nil {
		// Approver 本身失败不是“用户拒绝”。
		//
		// 返回 error，让 PolicyPipeline fail closed，
		// 同时保留真实故障语义。
		return po.BeforeToolCallDecision{}, fmt.Errorf("request approval: %w", err)
	}
	if !allowed {
		return po.BeforeToolCallDecision{Block: true, Reason: "user denied the requested operation"}, nil
	}
	return po.BeforeToolCallDecision{}, nil
}

var _ BeforeToolPolicy = (*Approval)(nil)
