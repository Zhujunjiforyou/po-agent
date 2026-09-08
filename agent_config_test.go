package po_test

import (
	"context"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
)

func TestNewAgentRequiresValidatorWhenToolsAreRegistered(t *testing.T) {
	model := scripted.MustNew(scripted.DefaultInfo(), scripted.Must(scripted.Reply("a1", "done", po.Usage{})))

	_, err := po.NewAgent(po.AgentConfig{
		Model: model,
		Tools: mustRegistry(t, mustReadTool(t, "read_file", func(_ context.Context, _ readArgs, _ po.ToolUpdateEmitter) (po.ToolResult, error) {
			return po.NewTextToolResult("ok", nil, false)
		})),
	})
	if err == nil {
		t.Fatal("expected missing validator to be rejected")
	}
}
