package po

import (
	"context"
	"fmt"
	"sync"
)

// ControlQueueMode 决定一次 Turn 边界上最多消费多少条排队消息。
//
// one-at-a-time 是默认值：每次只拿一条消息，让模型在新的 Observation 上重新决策。
// all 则一次把当前队列全部注入下一轮 Context，适合调用方明确希望批量合并指令的场景。
type ControlQueueMode string

const (
	ControlQueueOneAtATime ControlQueueMode = "one-at-a-time"
	ControlQueueAll        ControlQueueMode = "all"
)

func (m ControlQueueMode) Validate() error {
	switch m {
	case ControlQueueOneAtATime, ControlQueueAll:
		return nil
	default:
		return fmt.Errorf("invalid control queue mode %q", m)
	}
}

// RunHandle 是一次正在运行的 Agent Run 的控制句柄。
//
// Agent 本身仍然只保存可复用依赖；Steering、Follow-up 和 Abort 都绑定到具体 Run。
// 这样同一个 Agent 即使并发启动多个 Run，也不会出现“这条 steer 到底属于哪个 Run”的歧义。
type RunHandle struct {
	runID string

	runCtx context.Context
	cancel context.CancelCauseFunc

	control *runControl

	done chan struct{}

	mu     sync.Mutex
	result RunResult
	err    error
}

func newRunHandle(runID string, runCtx context.Context, cancel context.CancelCauseFunc, control *runControl) *RunHandle {
	return &RunHandle{
		runID:   runID,
		runCtx:  runCtx,
		cancel:  cancel,
		control: control,
		done:    make(chan struct{}),
	}
}

// RunID 在 Run 尚未结束时也可用，便于 CLI / Trace 立即建立关联。
func (h *RunHandle) RunID() string {
	return h.runID
}

// Done 在 Run 完成后关闭。调用方可以把它放进 select，而不必阻塞 Wait。
func (h *RunHandle) Done() <-chan struct{} {
	return h.done
}

// Wait 等待 Run 完成并返回与 Agent.Run 相同的结果。
//
// Wait 可以被多个 goroutine 调用；result/err 会在 done 关闭前一次性写入。
func (h *RunHandle) Wait() (RunResult, error) {
	<-h.done

	h.mu.Lock()
	defer h.mu.Unlock()
	return h.result, h.err
}

// Steer 把一条新的用户指令放入 Steering Queue。
//
// Steering 不会强行中断当前 Model Stream 或 Tool Execute。Po 和 Pi 一样，把它理解成
// “在当前 Turn 完整结束后，优先在下一次模型调用前注入的新指令”。
func (h *RunHandle) Steer(message UserMessage) error {
	if err := message.Validate(); err != nil {
		return fmt.Errorf("steering message: %w", err)
	}
	if cause := context.Cause(h.runCtx); cause != nil {
		return ErrRunClosed
	}
	return h.control.enqueueSteering(message)
}

// FollowUp 把一条用户消息放入 Follow-up Queue。
//
// Follow-up 不抢占当前工作。只有 Agent 本来准备自然结束，并且没有待处理 Steering 时，
// Runtime 才会消费 Follow-up 并开启新的 Turn。
func (h *RunHandle) FollowUp(message UserMessage) error {
	if err := message.Validate(); err != nil {
		return fmt.Errorf("follow-up message: %w", err)
	}
	if cause := context.Cause(h.runCtx); cause != nil {
		return ErrRunClosed
	}
	return h.control.enqueueFollowUp(message)
}

// Abort 主动取消当前 Run。
//
// Abort 是幂等的。它和 Steering 的语义完全不同：Steering 等待安全 Turn 边界后改变方向；
// Abort 则立即取消整棵 Context Tree，让 Model / Tool 尽快合作退出。
func (h *RunHandle) Abort() {
	// 先关闭控制队列，确保 Abort 之后新的 Steering / Follow-up 不会被错误地接受。
	h.control.close()
	h.cancel(ErrRunAborted)
}

func (h *RunHandle) complete(result RunResult, err error) {
	h.mu.Lock()
	h.result = result
	h.err = err
	h.mu.Unlock()
	close(h.done)
}

// runControl 只保存“外部希望在下一安全边界做什么”。
// Transcript 仍然只由 Run goroutine 修改，外部 goroutine 不能直接碰 runState。
type runControl struct {
	mu sync.Mutex

	active bool

	steeringMode ControlQueueMode
	followUpMode ControlQueueMode

	steering []UserMessage
	followUp []UserMessage
}

func newRunControl(steeringMode, followUpMode ControlQueueMode) *runControl {
	return &runControl{
		active:       true,
		steeringMode: steeringMode,
		followUpMode: followUpMode,
	}
}

func (c *runControl) enqueueSteering(message UserMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.active {
		return ErrRunClosed
	}

	c.steering = append(c.steering, message)
	return nil
}

func (c *runControl) enqueueFollowUp(message UserMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.active {
		return ErrRunClosed
	}

	c.followUp = append(c.followUp, message)
	return nil
}

// takeSteering 在“Run 本来仍会继续”的 Turn 边界调用。
//
// Follow-up 在这种情况下不能抢占自动 Tool follow-up；它只应该等 Agent 本来准备停下时再处理。
func (c *runControl) takeSteering() []UserMessage {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.active || len(c.steering) == 0 {
		return nil
	}
	return takeQueuedMessages(&c.steering, c.steeringMode)
}

// takeNextAtNaturalStop 用于 Agent 本来准备自然结束的边界。
//
// 优先级固定为 Steering > Follow-up。最重要的是“检查队列”和“关闭接受新消息”在同一把锁内：
// 如果这里返回 closed=true，就不存在一条并发 enqueue 已经返回 nil、却永远不会被处理的消息。
func (c *runControl) takeNextAtNaturalStop() (messages []UserMessage, closed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.active {
		return nil, true
	}

	if len(c.steering) > 0 {
		return takeQueuedMessages(&c.steering, c.steeringMode), false
	}
	if len(c.followUp) > 0 {
		return takeQueuedMessages(&c.followUp, c.followUpMode), false
	}

	c.active = false
	return nil, true
}

func (c *runControl) close() {
	c.mu.Lock()
	c.active = false
	c.steering = nil
	c.followUp = nil
	c.mu.Unlock()
}

func takeQueuedMessages(queue *[]UserMessage, mode ControlQueueMode) []UserMessage {
	if len(*queue) == 0 {
		return nil
	}

	if mode == ControlQueueAll {
		messages := append([]UserMessage(nil), (*queue)...)
		*queue = nil
		return messages
	}

	message := (*queue)[0]
	*queue = (*queue)[1:]
	return []UserMessage{message}
}
