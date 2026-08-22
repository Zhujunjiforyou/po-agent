package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
)

type chatMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string                 `json:"type"`
	Function chatFunctionDefinition `json:"function"`
}

type chatFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatToolCall struct {
	Index    int              `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatCompletion struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type chatChoice struct {
	Index        int                 `json:"index"`
	Message      chatResponseMessage `json:"message"`
	FinishReason string              `json:"finish_reason"`
}

type chatResponseMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content"`
	ToolCalls        []chatToolCall `json:"tool_calls"`
}

type chatCompletionChunk struct {
	ID      string            `json:"id"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   *chatUsage        `json:"usage,omitempty"`
	Error   *wireError        `json:"error,omitempty"`
}

type chatChunkChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type chatDelta struct {
	Role             string         `json:"role,omitempty"`
	Content          string         `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	Reasoning        string         `json:"reasoning,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
}

type chatUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u chatUsage) toPo() po.Usage {
	return po.Usage{
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		CacheReadTokens: u.PromptTokensDetails.CachedTokens,
	}
}

type errorEnvelope struct {
	Error wireError `json:"error"`
}

type wireError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func (e wireError) codeString() string {
	switch value := e.Code.(type) {
	case string:
		return value
	case float64:
		return fmt.Sprintf("%g", value)
	case nil:
		return ""
	default:
		payload, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(payload)
	}
}

func encodeMessages(systemPrompt string, messages []po.Message) ([]chatMessage, error) {
	result := make([]chatMessage, 0, len(messages)+1)
	if strings.TrimSpace(systemPrompt) != "" {
		result = append(result, chatMessage{Role: "system", Content: systemPrompt})
	}

	for index, message := range messages {
		switch typed := message.(type) {
		case po.UserMessage:
			result = append(result, chatMessage{Role: "user", Content: joinTextParts(typed.Parts())})

		case po.AssistantMessage:
			encoded := chatMessage{Role: "assistant"}
			for _, part := range typed.Parts() {
				switch part.Type {
				case po.ContentText:
					encoded.Content += part.Text
				case po.ContentThinking:
					encoded.ReasoningContent += part.Text
				case po.ContentToolCall:
					if part.ToolCall == nil {
						return nil, fmt.Errorf("encode assistant message %d: nil tool call", index)
					}
					call := part.ToolCall.Clone()
					encoded.ToolCalls = append(encoded.ToolCalls, chatToolCall{
						ID:   call.ID,
						Type: "function",
						Function: chatFunctionCall{
							Name:      call.Name,
							Arguments: string(call.Arguments),
						},
					})
				}
			}
			result = append(result, encoded)

		case po.ToolResultMessage:
			result = append(result, chatMessage{
				Role:       "tool",
				Content:    joinTextParts(typed.Parts()),
				ToolCallID: typed.ToolCallID(),
			})

		default:
			return nil, fmt.Errorf("encode message %d: unsupported message type %T", index, message)
		}
	}
	return result, nil
}

func joinTextParts(parts []po.ContentPart) string {
	var builder strings.Builder
	for _, part := range parts {
		if part.Type != po.ContentText {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(part.Text)
	}
	return builder.String()
}

func mapFinishReason(reason string) (po.ModelStopReason, error) {
	switch reason {
	case "stop", "end_turn":
		return po.ModelStopEndTurn, nil
	case "tool_calls", "function_call":
		return po.ModelStopToolCall, nil
	case "length", "max_tokens":
		return po.ModelStopLength, nil
	case "":
		return "", protocolError("stream ended without finish_reason", nil)
	default:
		return "", protocolError(fmt.Sprintf("unsupported finish_reason %q", reason), nil)
	}
}
