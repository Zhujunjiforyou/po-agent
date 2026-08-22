package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	provider "github.com/lemonzjj/po-agent-go/provider"
)

func testConfig(baseURL string) Config {
	return Config{
		Provider: "test",
		BaseURL:  baseURL,
		APIKey:   "secret",
		Model:    "test-model",
		Capabilities: po.ModelCapabilities{
			Streaming:         true,
			Tools:             true,
			ParallelToolCalls: true,
			Reasoning:         true,
		},
		Limits: po.ModelLimits{ContextWindow: 128000, MaxOutputTokens: 32000},
	}
}

func testRequest(t *testing.T, tools ...po.ToolSpec) po.ModelRequest {
	t.Helper()
	user, err := po.NewUserTextMessage("user-1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	request, err := po.NewModelRequest("system", []po.Message{user}, tools, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestGenerateStreamTextReasoningAndToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEData(w, `{"id":"resp-1","choices":[{"index":0,"delta":{"reasoning_content":"think "},"finish_reason":null}]}`)
		writeSSEData(w, `{"id":"resp-1","choices":[{"index":0,"delta":{"content":"I will calculate."},"finish_reason":null}]}`)
		writeSSEData(w, `{"id":"resp-1","choices":[{"index":0,"delta":{"tool_calls":[`+
			`{"index":0,"id":"call-1","type":"function","function":`+
			`{"name":"calculator","arguments":"{\"a\":17,"}}]},"finish_reason":null}]}`)
		writeSSEData(w, `{"id":"resp-1","choices":[{"index":0,"delta":{"tool_calls":[`+
			`{"index":0,"function":{"arguments":"\"b\":19}"}}]},"finish_reason":null}]}`)
		writeSSEData(w, `{"id":"resp-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],`+
			`"usage":{"prompt_tokens":10,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":3}}}`)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	config := testConfig(server.URL + "/v1")
	config.IncludeUsage = true
	model, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	var deltas []po.ModelDelta
	response, err := model.Generate(context.Background(), testRequest(t), func(ctx context.Context, delta po.ModelDelta) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason() != po.ModelStopToolCall {
		t.Fatalf("stop reason = %q", response.StopReason())
	}
	if response.Usage().InputTokens != 10 || response.Usage().CacheReadTokens != 3 {
		t.Fatalf("usage = %+v", response.Usage())
	}

	parts := response.Message().Parts()
	if len(parts) != 3 {
		t.Fatalf("parts = %#v", parts)
	}
	if parts[0].Type != po.ContentThinking || parts[0].Text != "think " {
		t.Fatalf("thinking part = %#v", parts[0])
	}
	if parts[1].Type != po.ContentText || parts[1].Text != "I will calculate." {
		t.Fatalf("text part = %#v", parts[1])
	}
	if parts[2].ToolCall == nil || parts[2].ToolCall.ID != "call-1" {
		t.Fatalf("tool part = %#v", parts[2])
	}
	if string(parts[2].ToolCall.Arguments) != `{"a":17,"b":19}` {
		t.Fatalf("arguments = %s", parts[2].ToolCall.Arguments)
	}

	kinds := make([]po.ModelDeltaKind, 0, len(deltas))
	for _, delta := range deltas {
		kinds = append(kinds, delta.Kind)
	}
	wantKinds := []po.ModelDeltaKind{
		po.ModelDeltaThinking,
		po.ModelDeltaText,
		po.ModelDeltaToolCallStart,
		po.ModelDeltaToolCallArguments,
		po.ModelDeltaToolCallArguments,
		po.ModelDeltaToolCallEnd,
	}
	if fmt.Sprint(kinds) != fmt.Sprint(wantKinds) {
		t.Fatalf("delta kinds = %v, want %v", kinds, wantKinds)
	}
}

func TestGenerateOnceMapsAssistantHistoryAndToolResult(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-2","choices":[`+
			`{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	}))
	defer server.Close()

	config := testConfig(server.URL + "/v1")
	config.Capabilities.Streaming = false
	model, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	call, err := po.NewToolCall("call-7", "calculator", map[string]any{"a": 1, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := po.NewAssistantMessage("assistant-1", po.ThinkingPart("reasoning"), po.ToolCallPart(call))
	if err != nil {
		t.Fatal(err)
	}
	toolResult, err := po.NewToolResultMessage("result-1", "call-7", "calculator", false, po.TextPart("3"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := po.NewModelRequest("sys", []po.Message{assistant, toolResult}, nil, 100)
	if err != nil {
		t.Fatal(err)
	}

	response, err := model.Generate(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason() != po.ModelStopEndTurn {
		t.Fatalf("stop reason = %q", response.StopReason())
	}

	messages, ok := captured["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("messages = %#v", captured["messages"])
	}
	assistantWire := messages[1].(map[string]any)
	if assistantWire["reasoning_content"] != "reasoning" {
		t.Fatalf("reasoning_content = %#v", assistantWire["reasoning_content"])
	}
	calls := assistantWire["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %#v", calls)
	}
	resultWire := messages[2].(map[string]any)
	if resultWire["role"] != "tool" || resultWire["tool_call_id"] != "call-7" {
		t.Fatalf("tool result = %#v", resultWire)
	}
}

func TestHTTP429BecomesRetryableProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit"}}`)
	}))
	defer server.Close()

	config := testConfig(server.URL + "/v1")
	model, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(context.Background(), testRequest(t), nil)
	if err == nil {
		t.Fatal("expected error")
	}

	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if !providerErr.Retryable || providerErr.StatusCode != 429 || providerErr.RetryAfter != 7*time.Second {
		t.Fatalf("provider error = %+v", providerErr)
	}
}

func TestExtraBodyCannotOverrideProtocolFields(t *testing.T) {
	config := testConfig("https://example.com/v1")
	config.ExtraBody = map[string]any{"messages": []any{}}
	_, err := New(config)
	if err == nil || !strings.Contains(err.Error(), "reserved key") {
		t.Fatalf("error = %v", err)
	}
}

func TestStreamingSyntheticToolCallIDWhenEndpointOmitsID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEData(w, `{"id":"resp-x","choices":[{"index":0,"delta":{"tool_calls":[`+
			`{"index":0,"function":{"name":"calculator","arguments":"{}"}}]},"finish_reason":null}]}`)
		_, _ = io.WriteString(w, "data: {\"id\":\"resp-x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	model, err := New(testConfig(server.URL + "/v1"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Generate(context.Background(), testRequest(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := response.Message().ToolCalls()
	if len(calls) != 1 || calls[0].ID != "resp-x-call-0" {
		t.Fatalf("calls = %#v", calls)
	}
}

func writeSSEData(w io.Writer, data string) {
	_, _ = io.WriteString(w, "data: "+data+"\n\n")
}
