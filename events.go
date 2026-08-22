package po

import (
	"context"
	"sync"
)

type AgentEventType string

const (
	EventRunStart      AgentEventType = "run_start"
	EventRunEnd        AgentEventType = "run_end"
	EventTurnStart     AgentEventType = "turn_start"
	EventTurnEnd       AgentEventType = "turn_end"
	EventMessageStart  AgentEventType = "message_start"
	EventMessageUpdate AgentEventType = "message_update"
	EventMessageEnd    AgentEventType = "message_end"
	EventToolStart     AgentEventType = "tool_start"
	EventToolUpdate    AgentEventType = "tool_update"
	EventToolEnd       AgentEventType = "tool_end"
)

// AgentEvent 是核心对外提供的封闭运行时事件协议。
type AgentEvent interface {
	Type() AgentEventType
	isAgentEvent()
}

type RunStartEvent struct{ RunID string }

func (RunStartEvent) Type() AgentEventType { return EventRunStart }
func (RunStartEvent) isAgentEvent()        {}

type RunEndEvent struct {
	RunID      string
	StopReason RunStopReason
	Err        error
}

func (RunEndEvent) Type() AgentEventType { return EventRunEnd }
func (RunEndEvent) isAgentEvent()        {}

type TurnStartEvent struct {
	RunID  string
	TurnID string
	Turn   int
}

func (TurnStartEvent) Type() AgentEventType { return EventTurnStart }
func (TurnStartEvent) isAgentEvent()        {}

type TurnEndEvent struct {
	RunID  string
	TurnID string
	Turn   int
}

func (TurnEndEvent) Type() AgentEventType { return EventTurnEnd }
func (TurnEndEvent) isAgentEvent()        {}

type MessageStartEvent struct {
	RunID  string
	TurnID string
}

func (MessageStartEvent) Type() AgentEventType { return EventMessageStart }
func (MessageStartEvent) isAgentEvent()        {}

type MessageUpdateEvent struct {
	RunID  string
	TurnID string
	Delta  ModelDelta
}

func (MessageUpdateEvent) Type() AgentEventType { return EventMessageUpdate }
func (MessageUpdateEvent) isAgentEvent()        {}

type MessageEndEvent struct {
	RunID   string
	TurnID  string
	Message AssistantMessage
}

func (MessageEndEvent) Type() AgentEventType { return EventMessageEnd }
func (MessageEndEvent) isAgentEvent()        {}

type ToolStartEvent struct {
	RunID      string
	TurnID     string
	ToolCallID string
	ToolName   string
}

func (ToolStartEvent) Type() AgentEventType { return EventToolStart }
func (ToolStartEvent) isAgentEvent()        {}

type ToolUpdateEvent struct {
	RunID      string
	TurnID     string
	ToolCallID string
	ToolName   string
	Update     ToolUpdate
}

func (ToolUpdateEvent) Type() AgentEventType { return EventToolUpdate }
func (ToolUpdateEvent) isAgentEvent()        {}

// ToolEndEvent 结束已经开始的工具生命周期。Err 只表示运行时或后处理失败；IsError 表示
// 模型可见的 ToolResult 语义。
type ToolEndEvent struct {
	RunID      string
	TurnID     string
	ToolCallID string
	ToolName   string
	Result     ToolResult
	IsError    bool
	Err        error
}

func (ToolEndEvent) Type() AgentEventType { return EventToolEnd }
func (ToolEndEvent) isAgentEvent()        {}

type EventHandler func(context.Context, AgentEvent)

type subscription struct {
	id      uint64
	handler EventHandler
}

// eventHub 是 Agent 持有的进程内同步分发器。它保持私有，调用方只能通过 Agent.Subscribe
// 观察运行时事件，不能主动驱动事件分发。
type eventHub struct {
	subscribersMu sync.RWMutex
	emitMu        sync.Mutex
	nextID        uint64
	subscribers   []subscription
}

func (h *eventHub) subscribe(handler EventHandler) func() {
	if handler == nil {
		return func() {}
	}

	h.subscribersMu.Lock()
	h.nextID++
	id := h.nextID
	h.subscribers = append(h.subscribers, subscription{id: id, handler: handler})
	h.subscribersMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			h.subscribersMu.Lock()
			defer h.subscribersMu.Unlock()
			for i, current := range h.subscribers {
				if current.id == id {
					h.subscribers = append(h.subscribers[:i], h.subscribers[i+1:]...)
					return
				}
			}
		})
	}
}

func (h *eventHub) emit(ctx context.Context, event AgentEvent) {
	if event == nil {
		return
	}

	h.emitMu.Lock()
	defer h.emitMu.Unlock()

	h.subscribersMu.RLock()
	snapshot := append([]subscription(nil), h.subscribers...)
	h.subscribersMu.RUnlock()

	for _, current := range snapshot {
		current.handler(ctx, event)
	}
}
