// Package guard 包含 Po 智能体可选的运行治理策略。
// Agent 核心不依赖本包。
package guard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	po "github.com/lemonzjj/po-agent-go"
)

const (
	StopMaxTurns     po.RunStopReason = "max_turns"
	StopMaxToolCalls po.RunStopReason = "max_tool_calls"
	StopInputTokens  po.RunStopReason = "input_token_budget"
	StopOutputTokens po.RunStopReason = "output_token_budget"
	StopCostBudget   po.RunStopReason = "cost_budget"
	StopNoProgress   po.RunStopReason = "no_progress"
)

// Budget 描述单次运行的可选限制；零值表示禁用对应限制。
type Budget struct {
	MaxTurns        int
	MaxToolCalls    int
	MaxInputTokens  int64
	MaxOutputTokens int64
	MaxCostUSD      float64
	MaxDuration     time.Duration
	NoProgressLimit int
}

func DefaultBudget() Budget {
	return Budget{MaxTurns: 32, MaxToolCalls: 128, NoProgressLimit: 3}
}

func (b Budget) Validate() error {
	if b.MaxTurns < 0 || b.MaxToolCalls < 0 || b.NoProgressLimit < 0 {
		return fmt.Errorf("invalid guard budget: count limits cannot be negative")
	}
	if b.MaxInputTokens < 0 || b.MaxOutputTokens < 0 {
		return fmt.Errorf("invalid guard budget: token limits cannot be negative")
	}
	if b.MaxDuration < 0 {
		return fmt.Errorf("invalid guard budget: max duration cannot be negative")
	}
	if math.IsNaN(b.MaxCostUSD) || math.IsInf(b.MaxCostUSD, 0) || b.MaxCostUSD < 0 {
		return fmt.Errorf("invalid guard budget: max cost must be a finite non-negative number")
	}
	return nil
}

// Controller 通过核心钩子实现可选的预算和无进展检测。它同时实现 Name 和
// BeforeToolCall，因此可以传给 policy.New。
type Controller struct {
	budget Budget
}

func New(budget Budget) (*Controller, error) {
	if err := budget.Validate(); err != nil {
		return nil, err
	}
	return &Controller{budget: budget}, nil
}

func MustNew(budget Budget) *Controller {
	controller, err := New(budget)
	if err != nil {
		panic(err)
	}
	return controller
}

func (c *Controller) Name() string { return "run-guard" }

// Context 在 Agent 核心外应用可选的运行时长限制。
func (c *Controller) Context(parent context.Context) (context.Context, context.CancelFunc) {
	if c.budget.MaxDuration <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeoutCause(parent, c.budget.MaxDuration, po.ErrRunTimeout)
}

// BeforeToolCall 保护副作用边界。在首次实际调用 Tool.Execute 前检查用量限制和过大的
// 工具批次。
func (c *Controller) BeforeToolCall(ctx context.Context, input po.BeforeToolCallContext) (po.BeforeToolCallDecision, error) {
	if cause := context.Cause(ctx); cause != nil {
		return po.BeforeToolCallDecision{}, cause
	}

	if reason, exceeded := c.usageReason(input.Run.Usage); exceeded {
		return stopDecision(reason, "tool call was not executed because the run usage budget was exhausted"), nil
	}
	if c.budget.MaxToolCalls > 0 && input.Run.ToolCalls+input.BatchSize > c.budget.MaxToolCalls {
		return stopDecision(StopMaxToolCalls, "tool call was not executed because the maximum tool-call budget would be exceeded"), nil
	}
	return po.BeforeToolCallDecision{}, nil
}

// ShouldStopAfterTurn 只会在核心原本准备开始下一轮模型调用时执行。
func (c *Controller) ShouldStopAfterTurn(ctx context.Context, input po.TurnCompletedContext) (po.RunStopReason, bool, error) {
	if cause := context.Cause(ctx); cause != nil {
		return "", false, cause
	}

	if reason, exceeded := c.usageReason(input.Run.Usage); exceeded {
		return reason, true, nil
	}
	if c.budget.MaxTurns > 0 && input.Run.Turn >= c.budget.MaxTurns {
		return StopMaxTurns, true, nil
	}
	if c.budget.MaxToolCalls > 0 && input.Run.ToolCalls > c.budget.MaxToolCalls {
		return StopMaxToolCalls, true, nil
	}
	if c.budget.NoProgressLimit > 0 {
		count, err := consecutiveRepeatedToolTurns(input.Run.Messages, c.budget.NoProgressLimit)
		if err != nil {
			return "", false, err
		}
		if count >= c.budget.NoProgressLimit {
			return StopNoProgress, true, nil
		}
	}
	return "", false, nil
}

func (c *Controller) usageReason(usage po.Usage) (po.RunStopReason, bool) {
	if c.budget.MaxInputTokens > 0 && usage.InputTokens > c.budget.MaxInputTokens {
		return StopInputTokens, true
	}
	if c.budget.MaxOutputTokens > 0 && usage.OutputTokens > c.budget.MaxOutputTokens {
		return StopOutputTokens, true
	}
	if c.budget.MaxCostUSD > 0 && usage.CostUSD > c.budget.MaxCostUSD {
		return StopCostBudget, true
	}
	return "", false
}

func stopDecision(reason po.RunStopReason, message string) po.BeforeToolCallDecision {
	return po.BeforeToolCallDecision{Block: true, Reason: message, StopReason: reason}
}

// consecutiveRepeatedToolTurns 扫描对话记录，而不保存每次运行的可变防护状态，因此
// Controller 可以安全地用于并发 Agent.Run 调用。
func consecutiveRepeatedToolTurns(messages []po.Message, limit int) (int, error) {
	index := len(messages)
	count := 0
	last := ""

	for index > 0 && count < limit {
		results := make([]po.ToolResultMessage, 0)
		for index > 0 {
			result, ok := asToolResult(messages[index-1])
			if !ok {
				break
			}
			results = append(results, result)
			index--
		}
		if len(results) == 0 || index == 0 {
			break
		}
		reverseToolResults(results)

		assistant, ok := asAssistant(messages[index-1])
		if !ok {
			break
		}
		index--

		calls := assistant.ToolCalls()
		if len(calls) == 0 || len(calls) != len(results) {
			break
		}
		fingerprint, err := fingerprintToolTurn(calls, results)
		if err != nil {
			return 0, err
		}
		if count == 0 {
			last = fingerprint
		} else if fingerprint != last {
			break
		}
		count++
	}
	return count, nil
}

func asAssistant(message po.Message) (po.AssistantMessage, bool) {
	value, ok := message.(po.AssistantMessage)
	return value, ok
}

func asToolResult(message po.Message) (po.ToolResultMessage, bool) {
	value, ok := message.(po.ToolResultMessage)
	return value, ok
}

func reverseToolResults(results []po.ToolResultMessage) {
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
}

func fingerprintToolTurn(calls []po.ToolCall, results []po.ToolResultMessage) (string, error) {
	var builder strings.Builder
	for i, call := range calls {
		canonical, err := canonicalJSON(call.Arguments)
		if err != nil {
			return "", err
		}
		builder.WriteString("call:")
		builder.WriteString(call.Name)
		builder.WriteByte(':')
		builder.Write(canonical)
		builder.WriteByte('\n')

		builder.WriteString("result:")
		if results[i].IsError() {
			builder.WriteString("error:")
		} else {
			builder.WriteString("ok:")
		}
		for _, part := range results[i].Parts() {
			if part.Type == po.ContentText {
				builder.WriteString(part.Text)
			}
		}
		builder.WriteByte('\n')
	}

	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:]), nil
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode tool arguments for fingerprint: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode canonical tool arguments: %w", err)
	}
	return canonical, nil
}
