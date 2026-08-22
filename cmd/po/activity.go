package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	po "github.com/lemonzjj/po-agent-go"
)

const maxActivityRunes = 56

type promptController struct {
	mu      sync.Mutex
	console replConsole

	activity string
	approval string
	running  bool
}

func newPromptController(console replConsole) *promptController {
	return &promptController{console: console}
}

func (p *promptController) SetRuntime(activity string, running bool) {
	p.mu.Lock()
	p.activity = compactActivity(activity)
	p.running = running
	state := p.stateLocked()
	p.mu.Unlock()
	_ = p.console.SetState(state)
}

func (p *promptController) SetApproval(toolName string) {
	p.mu.Lock()
	p.approval = compactActivity(toolName)
	state := p.stateLocked()
	p.mu.Unlock()
	_ = p.console.SetState(state)
}

func (p *promptController) Show() { _ = p.console.ShowPrompt() }

func (p *promptController) stateLocked() consoleState {
	return consoleState{Activity: p.activity, Approval: p.approval, Running: p.running}
}

type activeTool struct {
	id     string
	name   string
	detail string
}

// activityReporter 将运行时事件投影成一条简短的事实状态。它不会虚构工作流状态，也不会
// 把已完成工具保留成日志；状态提示始终描述最近一次可观察的运行事实。
type activityReporter struct {
	mu              sync.Mutex
	prompt          *promptController
	output          modelOutput
	turn            int
	tools           []activeTool
	running         bool
	preparingTool   string
	messageTextSeen bool
}

func newActivityReporter(prompt *promptController, output modelOutput) *activityReporter {
	return &activityReporter{prompt: prompt, output: output}
}

func (r *activityReporter) Observe(_ context.Context, event po.AgentEvent) {
	if event == nil {
		return
	}

	r.mu.Lock()
	status, fallbackText, finishMessage := r.observeLocked(event)
	running := r.running
	r.mu.Unlock()

	if fallbackText != "" {
		_ = r.output.WriteModelText(fallbackText)
	}
	if finishMessage {
		_ = r.output.FinishModelMessage()
	}
	r.prompt.SetRuntime(status, running)
}

func (r *activityReporter) Reset() {
	r.mu.Lock()
	r.turn = 0
	r.tools = nil
	r.running = false
	r.preparingTool = ""
	r.messageTextSeen = false
	r.mu.Unlock()
	r.prompt.SetRuntime("", false)
}

func (r *activityReporter) observeLocked(event po.AgentEvent) (string, string, bool) {
	switch current := event.(type) {
	case po.RunStartEvent:
		r.running = true
		r.tools = nil
		r.preparingTool = ""
		r.messageTextSeen = false
		return "starting", "", false
	case po.TurnStartEvent:
		r.turn = current.Turn
		return r.turnStatus("requesting model"), "", false
	case po.MessageStartEvent:
		r.preparingTool = ""
		r.messageTextSeen = false
		return r.turnStatus("waiting for response"), "", false
	case po.MessageUpdateEvent:
		switch current.Delta.Kind {
		case po.ModelDeltaThinking:
			return r.turnStatus("thinking"), "", false
		case po.ModelDeltaText:
			r.messageTextSeen = true
			return r.turnStatus("responding"), "", false
		case po.ModelDeltaToolCallStart:
			r.preparingTool = current.Delta.ToolName
			return r.preparingToolStatus(), "", false
		case po.ModelDeltaToolCallArguments, po.ModelDeltaToolCallEnd:
			return r.preparingToolStatus(), "", false
		default:
			return r.currentStatus(), "", false
		}
	case po.MessageEndEvent:
		fallback := ""
		if !r.messageTextSeen {
			fallback = current.Message.Text()
		}
		r.messageTextSeen = false
		return r.turnStatus("processing response"), fallback, true
	case po.ToolStartEvent:
		r.preparingTool = ""
		r.addTool(current.ToolCallID, current.ToolName)
		return r.toolStatus(), "", true
	case po.ToolUpdateEvent:
		r.updateTool(current.ToolCallID, current.Update)
		return r.toolStatus(), "", false
	case po.ToolEndEvent:
		r.removeTool(current.ToolCallID)
		if len(r.tools) > 0 {
			return r.toolStatus(), "", false
		}
		if current.Err != nil || current.IsError {
			return fmt.Sprintf("tool %s failed", current.ToolName), "", false
		}
		return fmt.Sprintf("tool %s done", current.ToolName), "", false
	case po.TurnEndEvent:
		return r.turnStatus("turn complete"), "", false
	case po.RunEndEvent:
		r.running = false
		r.tools = nil
		r.preparingTool = ""
		r.messageTextSeen = false
		return "", "", true
	default:
		return r.currentStatus(), "", false
	}
}

func (r *activityReporter) turnStatus(action string) string {
	if r.turn <= 0 {
		return action
	}
	return fmt.Sprintf("turn %d · %s", r.turn, action)
}

func (r *activityReporter) currentStatus() string {
	if len(r.tools) > 0 {
		return r.toolStatus()
	}
	if r.preparingTool != "" {
		return r.preparingToolStatus()
	}
	if r.running {
		return r.turnStatus("processing")
	}
	return ""
}

func (r *activityReporter) preparingToolStatus() string {
	if r.preparingTool == "" {
		return r.turnStatus("preparing tool call")
	}
	return fmt.Sprintf("preparing tool %s", r.preparingTool)
}

func (r *activityReporter) addTool(id, name string) {
	for index := range r.tools {
		if r.tools[index].id == id {
			r.tools[index].name = name
			return
		}
	}
	r.tools = append(r.tools, activeTool{id: id, name: name})
}

func (r *activityReporter) updateTool(id string, update po.ToolUpdate) {
	for index := range r.tools {
		if r.tools[index].id != id {
			continue
		}
		detail := compactActivity(update.Message)
		if update.Progress != nil {
			progress := fmt.Sprintf("%d%%", int(*update.Progress*100))
			if detail == "" {
				detail = progress
			} else {
				detail += " · " + progress
			}
		}
		r.tools[index].detail = detail
		return
	}
}

func (r *activityReporter) removeTool(id string) {
	for index := range r.tools {
		if r.tools[index].id == id {
			r.tools = append(r.tools[:index], r.tools[index+1:]...)
			return
		}
	}
}

func (r *activityReporter) toolStatus() string {
	if len(r.tools) == 1 {
		status := "tool " + r.tools[0].name
		if r.tools[0].detail != "" {
			status += " · " + r.tools[0].detail
		}
		return status
	}
	names := make([]string, 0, len(r.tools))
	for _, tool := range r.tools {
		names = append(names, tool.name)
	}
	return fmt.Sprintf("%d tools · %s", len(r.tools), strings.Join(names, ", "))
}

func compactActivity(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= maxActivityRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxActivityRunes-1]) + "…"
}
