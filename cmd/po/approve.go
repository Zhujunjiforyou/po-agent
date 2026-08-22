package main

import (
	"context"

	"github.com/lemonzjj/po-agent-go/policy"
)

type staticApprover bool

func (a staticApprover) Approve(context.Context, policy.ApprovalRequest) (bool, error) {
	return bool(a), nil
}

type approvalPrompt struct {
	Request policy.ApprovalRequest
	Reply   chan bool
}

type interactiveApprover struct{ requests chan approvalPrompt }

func newInteractiveApprover() *interactiveApprover {
	return &interactiveApprover{requests: make(chan approvalPrompt)}
}

func (a *interactiveApprover) Approve(ctx context.Context, req policy.ApprovalRequest) (bool, error) {
	reply := make(chan bool, 1)
	prompt := approvalPrompt{Request: req, Reply: reply}
	select {
	case a.requests <- prompt:
	case <-ctx.Done():
		return false, context.Cause(ctx)
	}
	select {
	case allowed := <-reply:
		return allowed, nil
	case <-ctx.Done():
		return false, context.Cause(ctx)
	}
}
