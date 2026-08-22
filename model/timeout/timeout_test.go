package timeout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	modeltimeout "github.com/lemonzjj/po-agent-go/model/timeout"
)

type blockingModel struct{}

func (blockingModel) Info() po.ModelInfo { return scripted.DefaultInfo() }
func (blockingModel) Generate(ctx context.Context, req po.ModelRequest, emit po.DeltaEmitter) (po.ModelResponse, error) {
	<-ctx.Done()
	return po.ModelResponse{}, ctx.Err()
}

func TestModelTimeoutReturnsStableCause(t *testing.T) {
	model, err := modeltimeout.New(blockingModel{}, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	user, _ := po.NewUserTextMessage("u1", "x")
	req, _ := po.NewModelRequest("", []po.Message{user}, nil, 0)
	_, err = model.Generate(context.Background(), req, nil)
	if !errors.Is(err, po.ErrModelTimeout) {
		t.Fatalf("error = %v", err)
	}
}

func TestSuccessfulModelCallIsNotTurnedIntoCancellationByCleanup(t *testing.T) {
	base := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Must(scripted.Reply("a1", "ok", po.Usage{})),
	)
	model, err := modeltimeout.New(base, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	user, err := po.NewUserTextMessage("u1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	request, err := po.NewModelRequest("", []po.Message{user}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	response, err := model.Generate(context.Background(), request, nil)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Message().Text() != "ok" {
		t.Fatalf("text = %q", response.Message().Text())
	}
}
