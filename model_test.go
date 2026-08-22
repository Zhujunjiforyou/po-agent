package po

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

// testModel 是最小的测试实现，用来证明 Model 接口不依赖任何 Provider SDK。
type testModel struct {
	info     ModelInfo
	response ModelResponse
}

func (m *testModel) Info() ModelInfo {
	return m.info
}

func (m *testModel) Generate(
	ctx context.Context,
	request ModelRequest,
	emit DeltaEmitter,
) (ModelResponse, error) {
	// 一个真实 Model 应先检查 Context 和请求能力，测试实现也遵守相同边界。
	if err := ctx.Err(); err != nil {
		return ModelResponse{}, err
	}
	if err := request.ValidateFor(m.info); err != nil {
		return ModelResponse{}, err
	}
	return m.response, nil
}

// 编译期接口断言：如果 testModel 的方法签名与 Model 不一致，编译将失败。
var _ Model = (*testModel)(nil)

func validModelInfo() ModelInfo {
	return ModelInfo{
		Provider:    "test",
		ID:          "scripted-v1",
		DisplayName: "Scripted Test Model",
		Capabilities: ModelCapabilities{
			Streaming:         true,
			Tools:             true,
			ParallelToolCalls: true,
		},
		Limits: ModelLimits{
			ContextWindow:   8192,
			MaxOutputTokens: 2048,
		},
	}
}

func mustUserTextForModel(t *testing.T, id, text string) UserMessage {
	t.Helper()
	message, err := NewUserTextMessage(id, text)
	if err != nil {
		t.Fatalf("NewUserTextMessage() error = %v", err)
	}
	return message
}

func mustAssistantText(t *testing.T, id, text string) AssistantMessage {
	t.Helper()
	message, err := NewAssistantMessage(id, TextPart(text))
	if err != nil {
		t.Fatalf("NewAssistantMessage() error = %v", err)
	}
	return message
}

func mustToolSpec(t *testing.T, name string, schema json.RawMessage) ToolSpec {
	t.Helper()
	spec, err := NewToolSpec(name, "test tool "+name, schema)
	if err != nil {
		t.Fatalf("NewToolSpec() error = %v", err)
	}
	return spec
}

func TestModelCapabilitiesRejectParallelToolsWithoutTools(t *testing.T) {
	capabilities := ModelCapabilities{
		Tools:             false,
		ParallelToolCalls: true,
	}

	err := capabilities.Validate()
	if !errors.Is(err, ErrInvalidModelInfo) {
		t.Fatalf("Validate() error = %v, want ErrInvalidModelInfo", err)
	}
}

func TestToolSpecOwnsSchemaBytes(t *testing.T) {
	original := json.RawMessage(`{"type":"object"}`)
	spec := mustToolSpec(t, "read_file", original)

	// 修改构造函数收到的原始 Slice，不应影响 ToolSpec 内部状态。
	original[2] = 'X'
	if got := string(spec.InputSchema()); got != `{"type":"object"}` {
		t.Fatalf("InputSchema() = %s, want original schema", got)
	}

	// 修改 getter 返回值，同样不应回写 ToolSpec。
	returned := spec.InputSchema()
	returned[2] = 'Y'
	if got := string(spec.InputSchema()); got != `{"type":"object"}` {
		t.Fatalf("InputSchema() changed through getter: %s", got)
	}
}

func TestModelRequestCopiesInputSlices(t *testing.T) {
	messages := []Message{mustUserTextForModel(t, "msg-user-1", "hello")}
	tools := []ToolSpec{
		mustToolSpec(t, "read_file", json.RawMessage(`{"type":"object"}`)),
	}

	request, err := NewModelRequest("system", messages, tools, 512)
	if err != nil {
		t.Fatalf("NewModelRequest() error = %v", err)
	}

	// 替换调用者原 Slice 中的元素，不应修改 request 保存的快照。
	messages[0] = mustUserTextForModel(t, "msg-user-2", "changed")
	tools[0] = mustToolSpec(t, "other", json.RawMessage(`{"type":"object"}`))

	if got := request.Messages()[0].MessageID(); got != "msg-user-1" {
		t.Fatalf("request message id = %q, want msg-user-1", got)
	}
	if got := request.Tools()[0].Name(); got != "read_file" {
		t.Fatalf("request tool name = %q, want read_file", got)
	}
}

func TestModelRequestValidateForRejectsUnsupportedTools(t *testing.T) {
	request, err := NewModelRequest(
		"",
		[]Message{mustUserTextForModel(t, "msg-user-1", "hello")},
		[]ToolSpec{mustToolSpec(t, "read_file", json.RawMessage(`{"type":"object"}`))},
		0,
	)
	if err != nil {
		t.Fatalf("NewModelRequest() error = %v", err)
	}

	info := validModelInfo()
	info.Capabilities.Tools = false
	info.Capabilities.ParallelToolCalls = false

	err = request.ValidateFor(info)
	if !errors.Is(err, ErrInvalidModelRequest) {
		t.Fatalf("ValidateFor() error = %v, want ErrInvalidModelRequest", err)
	}
}

func TestModelResponseRequiresToolCallForToolStop(t *testing.T) {
	_, err := NewModelResponse(
		mustAssistantText(t, "msg-assistant-1", "I forgot the tool call"),
		ModelStopToolCall,
		Usage{},
		"response-1",
	)
	if !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("NewModelResponse() error = %v, want ErrInvalidModelResponse", err)
	}
}

func TestModelResponseRejectsToolCallOnEndTurn(t *testing.T) {
	call, err := NewToolCall("call-1", "read_file", map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatalf("NewToolCall() error = %v", err)
	}
	message, err := NewAssistantMessage("msg-assistant-1", ToolCallPart(call))
	if err != nil {
		t.Fatalf("NewAssistantMessage() error = %v", err)
	}

	_, err = NewModelResponse(message, ModelStopEndTurn, Usage{}, "response-1")
	if !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("NewModelResponse() error = %v, want ErrInvalidModelResponse", err)
	}
}

func TestUsageAddDoesNotDoubleCountCacheTokens(t *testing.T) {
	first := Usage{
		InputTokens:      100,
		OutputTokens:     20,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		CostUSD:          0.01,
	}
	second := Usage{
		InputTokens:     50,
		OutputTokens:    15,
		CacheReadTokens: 20,
		CostUSD:         0.02,
	}

	total := first.Add(second)
	if got, want := total.TotalTokens(), int64(185); got != want {
		t.Fatalf("TotalTokens() = %d, want %d", got, want)
	}
	if got, want := total.CacheReadTokens, int64(60); got != want {
		t.Fatalf("CacheReadTokens = %d, want %d", got, want)
	}
}

func TestEmitModelDeltaValidatesAndPropagatesEmitterError(t *testing.T) {
	wantErr := errors.New("consumer stopped")
	emit := func(ctx context.Context, delta ModelDelta) error {
		return wantErr
	}

	err := EmitModelDelta(context.Background(), emit, ModelDelta{
		Kind:         ModelDeltaText,
		ContentIndex: 0,
		Text:         "hello",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("EmitModelDelta() error = %v, want %v", err, wantErr)
	}
}

func TestModelInterfaceCanBeUsedWithoutProviderSDK(t *testing.T) {
	message := mustAssistantText(t, "msg-assistant-1", "hello")
	response, err := NewModelResponse(message, ModelStopEndTurn, Usage{}, "response-1")
	if err != nil {
		t.Fatalf("NewModelResponse() error = %v", err)
	}

	var model Model = &testModel{
		info:     validModelInfo(),
		response: response,
	}
	request, err := NewModelRequest(
		"You are helpful.",
		[]Message{mustUserTextForModel(t, "msg-user-1", "hello")},
		nil,
		128,
	)
	if err != nil {
		t.Fatalf("NewModelRequest() error = %v", err)
	}

	got, err := model.Generate(context.Background(), request, nil)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got.Message().Text() != "hello" {
		t.Fatalf("response text = %q, want hello", got.Message().Text())
	}
}

func TestUsageRejectsNonFiniteCost(t *testing.T) {
	for _, cost := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := (Usage{CostUSD: cost}).Validate(); err == nil {
			t.Fatalf("Usage.Validate() accepted non-finite cost %v", cost)
		}
	}
}
