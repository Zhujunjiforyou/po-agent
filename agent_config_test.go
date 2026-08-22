package po_test

import (
	"context"
	"encoding/json"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
)

type typedNilValidator struct{}

func (*typedNilValidator) Validate(json.RawMessage, json.RawMessage) error { return nil }

func TestNewAgentRejectsTypedNilValidatorWhenToolsAreRegistered(t *testing.T) {
	var validator *typedNilValidator
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.Reply("a1", "done", po.Usage{})))

	_, err := po.NewAgent(po.AgentConfig{
		Model: model,
		Tools: mustRegistry(t, mustReadTool(t, "read_file", func(_ context.Context, _ readArgs, _ po.ToolUpdateEmitter) (po.ToolResult, error) {
			return po.NewTextToolResult("ok", nil, false)
		})),
		Validator: validator,
	})
	if err == nil {
		t.Fatal("expected typed nil validator to be rejected")
	}
}

func TestValidateToolCallArgumentsRejectsTypedNilValidator(t *testing.T) {
	var validator *typedNilValidator
	call, err := po.NewToolCall("c1", "read_file", map[string]any{"path": "README.md"})
	if err != nil {
		t.Fatal(err)
	}

	if err := po.ValidateToolCallArguments(validator, mustSpec(t, "read_file"), call); err == nil {
		t.Fatal("expected typed nil validator to be rejected")
	}
}
