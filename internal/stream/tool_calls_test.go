package stream

import (
	"errors"
	"testing"

	po "github.com/lemonzjj/po-agent-go"
)

func TestToolCallsBuildByContentIndex(
	t *testing.T,
) {
	assembler :=
		NewToolCalls(
			1024,
		)

	deltas :=
		[]po.ModelDelta{
			{
				Kind: po.ModelDeltaToolCallStart,

				ContentIndex: 1,

				ToolCallID: "call-b",

				ToolName: "read_file",
			},
			{
				Kind: po.ModelDeltaToolCallStart,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ToolName: "read_file",
			},
			{
				Kind: po.ModelDeltaToolCallArguments,

				ContentIndex: 1,

				ToolCallID: "call-b",

				ArgumentsDelta: `{"path":"b.go"}`,
			},
			{
				Kind: po.ModelDeltaToolCallEnd,

				ContentIndex: 1,

				ToolCallID: "call-b",
			},
			{
				Kind: po.ModelDeltaToolCallArguments,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ArgumentsDelta: `{"path":"a.go"}`,
			},
			{
				Kind: po.ModelDeltaToolCallEnd,

				ContentIndex: 0,

				ToolCallID: "call-a",
			},
		}

	for _, delta := range deltas {
		if err :=
			assembler.Apply(
				delta,
			); err != nil {
			t.Fatal(err)
		}
	}

	calls, err :=
		assembler.Build(
			po.ModelStopToolCall,
		)

	if err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 {
		t.Fatalf(
			"calls = %d, want 2",
			len(calls),
		)
	}

	if calls[0].ID !=
		"call-a" {
		t.Fatalf(
			"first call = %q",
			calls[0].ID,
		)
	}

	if calls[1].ID !=
		"call-b" {
		t.Fatalf(
			"second call = %q",
			calls[1].ID,
		)
	}
}

func TestToolCallsArgumentsCanArriveInFragments(
	t *testing.T,
) {
	assembler :=
		NewToolCalls(
			1024,
		)

	deltas :=
		[]po.ModelDelta{
			{
				Kind: po.ModelDeltaToolCallStart,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ToolName: "read_file",
			},
			{
				Kind: po.ModelDeltaToolCallArguments,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ArgumentsDelta: `{"pa`,
			},
			{
				Kind: po.ModelDeltaToolCallArguments,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ArgumentsDelta: `th":"main.go"}`,
			},
			{
				Kind: po.ModelDeltaToolCallEnd,

				ContentIndex: 0,

				ToolCallID: "call-a",
			},
		}

	for _, delta := range deltas {
		if err :=
			assembler.Apply(
				delta,
			); err != nil {
			t.Fatal(err)
		}
	}

	calls, err :=
		assembler.Build(
			po.ModelStopToolCall,
		)

	if err != nil {
		t.Fatal(err)
	}

	if string(
		calls[0].Arguments,
	) !=
		`{"path":"main.go"}` {
		t.Fatalf(
			"arguments = %s",
			calls[0].Arguments,
		)
	}
}

func TestLengthTruncatedToolCallsRejectedEvenIfJSONLooksValid(
	t *testing.T,
) {
	assembler :=
		NewToolCalls(
			1024,
		)

	deltas :=
		[]po.ModelDelta{
			{
				Kind: po.ModelDeltaToolCallStart,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ToolName: "write_file",
			},
			{
				Kind: po.ModelDeltaToolCallArguments,

				ContentIndex: 0,

				ToolCallID: "call-a",

				// 这段 JSON 自身已经合法，
				// 但整个模型 Response 最终 StopLength，
				// 仍然不能推断模型完整意图。
				ArgumentsDelta: `{"path":"a.go"}`,
			},
			{
				Kind: po.ModelDeltaToolCallEnd,

				ContentIndex: 0,

				ToolCallID: "call-a",
			},
		}

	for _, delta := range deltas {
		if err :=
			assembler.Apply(
				delta,
			); err != nil {
			t.Fatal(err)
		}
	}

	_, err :=
		assembler.Build(
			po.ModelStopLength,
		)

	if !errors.Is(
		err,
		ErrTruncatedToolCalls,
	) {
		t.Fatalf(
			"error = %v, want ErrTruncatedToolCalls",
			err,
		)
	}
}

func TestToolCallsRejectsArgumentsAfterEnd(
	t *testing.T,
) {
	assembler :=
		NewToolCalls(
			1024,
		)

	if err :=
		assembler.Apply(
			po.ModelDelta{
				Kind: po.ModelDeltaToolCallStart,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ToolName: "read_file",
			},
		); err != nil {
		t.Fatal(err)
	}

	if err :=
		assembler.Apply(
			po.ModelDelta{
				Kind: po.ModelDeltaToolCallEnd,

				ContentIndex: 0,

				ToolCallID: "call-a",
			},
		); err != nil {
		t.Fatal(err)
	}

	err :=
		assembler.Apply(
			po.ModelDelta{
				Kind: po.ModelDeltaToolCallArguments,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ArgumentsDelta: `{}`,
			},
		)

	if !errors.Is(
		err,
		ErrDuplicateToolCall,
	) {
		t.Fatalf(
			"error = %v, want ErrDuplicateToolCall",
			err,
		)
	}
}

func TestToolCallsRejectsOversizedArguments(
	t *testing.T,
) {
	assembler :=
		NewToolCalls(
			4,
		)

	if err :=
		assembler.Apply(
			po.ModelDelta{
				Kind: po.ModelDeltaToolCallStart,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ToolName: "read_file",
			},
		); err != nil {
		t.Fatal(err)
	}

	err :=
		assembler.Apply(
			po.ModelDelta{
				Kind: po.ModelDeltaToolCallArguments,

				ContentIndex: 0,

				ToolCallID: "call-a",

				ArgumentsDelta: `{"path":"too-long"}`,
			},
		)

	if !errors.Is(
		err,
		ErrToolCallTooLarge,
	) {
		t.Fatalf(
			"error = %v, want ErrToolCallTooLarge",
			err,
		)
	}
}
