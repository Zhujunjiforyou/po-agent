package openai

import (
	"context"
	"sort"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/internal/stream"
)

type streamPartKind uint8

const (
	streamPartThinking streamPartKind = iota + 1
	streamPartText
	streamPartTool
)

type streamToolState struct {
	wireIndex    int
	contentIndex int
	id           string
	name         strings.Builder
	pendingArgs  strings.Builder
	started      bool
}

type streamState struct {
	model *Model
	emit  po.DeltaEmitter

	responseID   string
	usage        po.Usage
	finishReason string

	nextContentIndex int
	thinkingIndex    int
	textIndex        int
	thinking         strings.Builder
	text             strings.Builder
	tools            map[int]*streamToolState
	partKinds        map[int]streamPartKind
	assembler        *stream.ToolCalls
}

func newStreamState(model *Model, emit po.DeltaEmitter) *streamState {
	return &streamState{
		model:         model,
		emit:          emit,
		thinkingIndex: -1,
		textIndex:     -1,
		tools:         make(map[int]*streamToolState),
		partKinds:     make(map[int]streamPartKind),
		assembler:     stream.NewToolCalls(model.config.MaxToolArgumentBytes),
	}
}

func (s *streamState) allocate(kind streamPartKind) int {
	index := s.nextContentIndex
	s.nextContentIndex++
	s.partKinds[index] = kind
	return index
}

func (s *streamState) apply(ctx context.Context, chunk chatCompletionChunk) error {
	if chunk.ID != "" {
		s.responseID = chunk.ID
	}
	if chunk.Usage != nil {
		s.usage = chunk.Usage.toPo()
	}

	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			// Po 协议 v1 只接收一个助手候选结果。多个候选属于产品层的采样能力，
			// 不应由 Agent 循环处理。
			continue
		}
		if choice.Delta.ReasoningContent != "" || choice.Delta.Reasoning != "" {
			text := choice.Delta.ReasoningContent
			if text == "" {
				text = choice.Delta.Reasoning
			}
			if s.thinkingIndex < 0 {
				s.thinkingIndex = s.allocate(streamPartThinking)
			}
			s.thinking.WriteString(text)
			if err := s.emitDelta(ctx, po.ModelDelta{Kind: po.ModelDeltaThinking, ContentIndex: s.thinkingIndex, Text: text}); err != nil {
				return err
			}
		}
		if choice.Delta.Content != "" {
			if s.textIndex < 0 {
				s.textIndex = s.allocate(streamPartText)
			}
			s.text.WriteString(choice.Delta.Content)
			if err := s.emitDelta(ctx, po.ModelDelta{Kind: po.ModelDeltaText, ContentIndex: s.textIndex, Text: choice.Delta.Content}); err != nil {
				return err
			}
		}
		for _, delta := range choice.Delta.ToolCalls {
			if err := s.applyToolDelta(ctx, delta); err != nil {
				return err
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			s.finishReason = *choice.FinishReason
		}
	}
	return nil
}

func (s *streamState) applyToolDelta(ctx context.Context, delta chatToolCall) error {
	state, exists := s.tools[delta.Index]
	if !exists {
		state = &streamToolState{wireIndex: delta.Index, contentIndex: s.allocate(streamPartTool)}
		s.tools[delta.Index] = state
	}
	if delta.ID != "" {
		if state.id != "" && state.id != delta.ID {
			return protocolError("tool call id changed during stream", nil)
		}
		state.id = delta.ID
	}
	if delta.Function.Name != "" {
		if state.started {
			return protocolError("tool call name changed after argument streaming started", nil)
		}
		state.name.WriteString(delta.Function.Name)
	}
	if delta.Function.Arguments == "" {
		return nil
	}

	if !state.started {
		if state.name.Len() == 0 {
			// 部分兼容实现可能先返回参数字节，后返回函数名。这里暂存数据，直到能够发出
			// 有效的开始增量。
			state.pendingArgs.WriteString(delta.Function.Arguments)
			return nil
		}
		if err := s.startTool(ctx, state); err != nil {
			return err
		}
	}
	return s.emitToolArgs(ctx, state, delta.Function.Arguments)
}

func (s *streamState) startTool(ctx context.Context, state *streamToolState) error {
	if state.started {
		return nil
	}
	if state.id == "" {
		state.id = s.model.syntheticToolCallID(s.responseID, state.wireIndex)
	}
	name := state.name.String()
	if name == "" {
		return protocolError("tool call is missing function name", nil)
	}
	state.started = true
	if err := s.emitDelta(ctx, po.ModelDelta{
		Kind:         po.ModelDeltaToolCallStart,
		ContentIndex: state.contentIndex,
		ToolCallID:   state.id,
		ToolName:     name,
	}); err != nil {
		return err
	}
	if state.pendingArgs.Len() > 0 {
		pending := state.pendingArgs.String()
		state.pendingArgs.Reset()
		if err := s.emitToolArgs(ctx, state, pending); err != nil {
			return err
		}
	}
	return nil
}

func (s *streamState) emitToolArgs(ctx context.Context, state *streamToolState, arguments string) error {
	return s.emitDelta(ctx, po.ModelDelta{
		Kind:           po.ModelDeltaToolCallArguments,
		ContentIndex:   state.contentIndex,
		ToolCallID:     state.id,
		ToolName:       state.name.String(),
		ArgumentsDelta: arguments,
	})
}

func (s *streamState) emitDelta(ctx context.Context, delta po.ModelDelta) error {
	if err := s.assembler.Apply(delta); err != nil {
		return protocolError("assemble streamed tool call", err)
	}
	if err := po.EmitModelDelta(ctx, s.emit, delta); err != nil {
		return err
	}
	return nil
}

func (s *streamState) finish(ctx context.Context) (po.ModelResponse, error) {
	stop, err := mapFinishReason(s.finishReason)
	if err != nil {
		return po.ModelResponse{}, err
	}

	toolStates := make([]*streamToolState, 0, len(s.tools))
	for _, state := range s.tools {
		toolStates = append(toolStates, state)
	}
	sort.Slice(toolStates, func(i, j int) bool { return toolStates[i].contentIndex < toolStates[j].contentIndex })
	for _, state := range toolStates {
		if !state.started {
			if err := s.startTool(ctx, state); err != nil {
				return po.ModelResponse{}, err
			}
		}
		if err := s.emitDelta(ctx, po.ModelDelta{
			Kind:         po.ModelDeltaToolCallEnd,
			ContentIndex: state.contentIndex,
			ToolCallID:   state.id,
			ToolName:     state.name.String(),
		}); err != nil {
			return po.ModelResponse{}, err
		}
	}

	calls, err := s.assembler.Build(stop)
	if err != nil {
		return po.ModelResponse{}, protocolError("finalize streamed tool calls", err)
	}
	callByContentIndex := make(map[int]po.ToolCall, len(calls))
	for index, state := range toolStates {
		if index >= len(calls) {
			return po.ModelResponse{}, protocolError("tool call assembler lost a streamed call", nil)
		}
		callByContentIndex[state.contentIndex] = calls[index]
	}

	parts := make([]po.ContentPart, 0, len(s.partKinds))
	for index := 0; index < s.nextContentIndex; index++ {
		switch s.partKinds[index] {
		case streamPartThinking:
			if s.thinking.Len() > 0 {
				parts = append(parts, po.ThinkingPart(s.thinking.String()))
			}
		case streamPartText:
			if s.text.Len() > 0 {
				parts = append(parts, po.TextPart(s.text.String()))
			}
		case streamPartTool:
			call, ok := callByContentIndex[index]
			if !ok {
				return po.ModelResponse{}, protocolError("missing finalized tool call", nil)
			}
			parts = append(parts, po.ToolCallPart(call))
		}
	}
	if len(parts) == 0 {
		return po.ModelResponse{}, protocolError("stream produced no assistant content", nil)
	}

	message, err := po.NewAssistantMessage(messageID(s.responseID), parts...)
	if err != nil {
		return po.ModelResponse{}, protocolError("build streamed assistant message", err)
	}
	response, err := po.NewModelResponse(message, stop, s.usage, s.responseID)
	if err != nil {
		return po.ModelResponse{}, protocolError("build streamed model response", err)
	}
	return response, nil
}
