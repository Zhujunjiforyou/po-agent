package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/contextwindow"
	"github.com/lemonzjj/po-agent-go/internal/appconfig"
	"github.com/lemonzjj/po-agent-go/session"
)

// 该测试会消耗真实模型 Token，因此只在显式启用时运行。
func TestLiveRepeatedContextCompressionQuality(t *testing.T) {
	if os.Getenv("PO_LIVE_CONTEXT_TEST") != "1" {
		t.Skip("set PO_LIVE_CONTEXT_TEST=1 to run against a real model")
	}
	baseURL, modelID := os.Getenv("PO_LIVE_BASE_URL"), os.Getenv("PO_LIVE_MODEL")
	if baseURL == "" || modelID == "" {
		t.Fatal("PO_LIVE_BASE_URL and PO_LIVE_MODEL are required")
	}
	key := os.Getenv("PO_LIVE_API_KEY")
	if key == "" {
		key = os.Getenv("PO_API_KEY")
	}

	config := appconfig.DefaultRuntime("live", appconfig.Provider{BaseURL: baseURL}, modelID)
	config.ContextWindow, config.ModelMaxOutputTokens, config.MaxOutputTokens = 8192, 4096, 512
	config.Streaming, config.Tools, config.ParallelToolCalls = false, false, false
	zero := 0.0
	config.Temperature = &zero
	var stdout, stderr bytes.Buffer
	runtime, err := buildAppRuntime(config, key, runtimeOptions{
		Workspace: t.TempDir(), Approver: staticApprover(false), Output: newPlainModelOutput(&stdout, &stderr),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	options, err := runtime.NewSessionOptions()
	if err != nil {
		t.Fatal(err)
	}
	builder := options.ContextBuilder.(*contextwindow.Builder)
	sess, err := session.NewWithOptions("live-context", time.Now().UTC(), nil, options)
	if err != nil {
		t.Fatal(err)
	}

	checks := map[int][]string{
		4:  {"ORCHID-731", "Lin", "43127", "NEVER_DELETE"},
		9:  {"ORCHID-731", "Mei", "43127", "NEVER_DELETE"},
		13: {"ORCHID-731", "Mei", "43128", "NEVER_DELETE"},
	}
	checkpoints := map[string]bool{}
	started := time.Now()
	for turn := 0; turn < 14; turn++ {
		fact := ""
		switch turn {
		case 0:
			fact = "PROJECT_CODE=ORCHID-731 OWNER=Lin PORT=43127 BACKUP_POLICY=NEVER_DELETE"
		case 6:
			fact = "CORRECTION: OWNER=Mei"
		case 10:
			fact = "CORRECTION: PORT=43128"
		}
		instruction := "Reply in at most 12 words."
		if want := checks[turn]; want != nil {
			instruction = "Return one line containing: " + strings.Join(want, " ")
		}
		log := fmt.Sprintf("WARN scheduler queue=%d path=session/live.jsonl retry=2\n", turn)
		prompt := fmt.Sprintf("%s\n%s\n%s\n%s", fact, instruction, strings.Repeat(log, 40), realisticProtocolTurn(turn, false))
		user, _ := po.NewUserTextMessage(fmt.Sprintf("live-user-%d", turn), prompt)
		turnCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		result, runErr := sess.Prompt(turnCtx, runtime.Agent, user)
		cancel()
		if runErr != nil {
			t.Fatalf("turn %d: %v", turn, runErr)
		}
		for _, want := range checks[turn] {
			if !strings.Contains(result.FinalText(), want) {
				t.Fatalf("turn %d lost %q: %q", turn, want, result.FinalText())
			}
		}
		if checkpoint, ok := builder.Checkpoint(); ok {
			checkpoints[checkpoint.FirstKeptMessageID] = true
			if !strings.Contains(checkpoint.Summary, "ORCHID-731") || !strings.Contains(checkpoint.Summary, "NEVER_DELETE") {
				t.Fatalf("summary lost stable facts: %q", checkpoint.Summary)
			}
		}
	}
	if len(checkpoints) < 2 {
		t.Fatalf("compactions=%d, want repeated compression", len(checkpoints))
	}
	t.Logf("model=%s turns=14 compactions=%d elapsed=%s", modelID, len(checkpoints), time.Since(started))
}
