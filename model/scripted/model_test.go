package scripted

import (
	"context"
	"errors"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

func testRequest(t *testing.T, messages ...po.Message) po.ModelRequest {
	t.Helper()
	request, err := po.NewModelRequest("test system", messages, nil, 128)
	if err != nil {
		t.Fatalf("NewModelRequest() error = %v", err)
	}
	return request
}

func testUser(t *testing.T, id, text string) po.UserMessage {
	t.Helper()
	message, err := po.NewUserTextMessage(id, text)
	if err != nil {
		t.Fatalf("NewUserTextMessage() error = %v", err)
	}
	return message
}

func TestModelConsumesStepsInOrderAndRecordsRequests(t *testing.T) {
	model := MustNew(
		DefaultInfo(),
		Must(Reply("assistant-1", "first", po.Usage{})),
		Must(Reply("assistant-2", "second", po.Usage{})),
	)

	first, err := model.Generate(
		context.Background(),
		testRequest(t, testUser(t, "user-1", "hello")),
		nil,
	)
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	second, err := model.Generate(
		context.Background(),
		testRequest(t, testUser(t, "user-2", "again")),
		nil,
	)
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}

	if got := first.Message().Text(); got != "first" {
		t.Fatalf("first text = %q, want first", got)
	}
	if got := second.Message().Text(); got != "second" {
		t.Fatalf("second text = %q, want second", got)
	}
	if got := model.CallCount(); got != 2 {
		t.Fatalf("CallCount() = %d, want 2", got)
	}
	if got := model.RemainingSteps(); got != 0 {
		t.Fatalf("RemainingSteps() = %d, want 0", got)
	}
	if got := len(model.Requests()); got != 2 {
		t.Fatalf("len(Requests()) = %d, want 2", got)
	}
}

func TestModelEmitsDeltasBeforeFinalResponse(t *testing.T) {
	model := MustNew(
		DefaultInfo(),
		Must(StreamText("assistant-1", []string{"你", "好"}, po.Usage{})),
	)

	var chunks []string
	response, err := model.Generate(
		context.Background(),
		testRequest(t, testUser(t, "user-1", "hello")),
		func(ctx context.Context, delta po.ModelDelta) error {
			chunks = append(chunks, delta.Text)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if got := len(chunks); got != 2 {
		t.Fatalf("delta count = %d, want 2", got)
	}
	if got := response.Message().Text(); got != "你好" {
		t.Fatalf("final text = %q, want 你好", got)
	}
}

func TestRequestCheckCanAssertToolResultWasAppended(t *testing.T) {
	toolResult, err := po.NewToolResultMessage(
		"result-1",
		"call-1",
		"read_file",
		false,
		po.TextPart("file contents"),
	)
	if err != nil {
		t.Fatalf("NewToolResultMessage() error = %v", err)
	}

	step := WithCheck(
		Must(Reply("assistant-1", "done", po.Usage{})),
		ExpectLastToolResult("call-1"),
	)
	model := MustNew(DefaultInfo(), step)

	_, err = model.Generate(
		context.Background(),
		testRequest(t, testUser(t, "user-1", "read"), toolResult),
		nil,
	)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestModelReturnsNoRemainingStep(t *testing.T) {
	model := MustNew(DefaultInfo())
	_, err := model.Generate(
		context.Background(),
		testRequest(t, testUser(t, "user-1", "hello")),
		nil,
	)
	if !errors.Is(err, ErrNoRemainingStep) {
		t.Fatalf("Generate() error = %v, want ErrNoRemainingStep", err)
	}
}

func TestCancelledCallDoesNotConsumeStep(t *testing.T) {
	model := MustNew(
		DefaultInfo(),
		Must(Reply("assistant-1", "still available", po.Usage{})),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := model.Generate(
		ctx,
		testRequest(t, testUser(t, "user-1", "hello")),
		nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() error = %v, want context.Canceled", err)
	}
	if got := model.RemainingSteps(); got != 1 {
		t.Fatalf("RemainingSteps() = %d, want 1", got)
	}
}

func TestEmitterErrorStopsGenerate(t *testing.T) {
	model := MustNew(
		DefaultInfo(),
		Must(StreamText("assistant-1", []string{"hello"}, po.Usage{})),
	)
	wantErr := errors.New("UI closed")

	_, err := model.Generate(
		context.Background(),
		testRequest(t, testUser(t, "user-1", "hello")),
		func(context.Context, po.ModelDelta) error {
			return wantErr
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Generate() error = %v, want %v", err, wantErr)
	}
}
