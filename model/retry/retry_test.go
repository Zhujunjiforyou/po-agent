package retry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
	"github.com/lemonzjj/po-agent-go/model/retry"
	"github.com/lemonzjj/po-agent-go/model/scripted"
	provider "github.com/lemonzjj/po-agent-go/provider"
)

type recordingSleeper struct{ delays []time.Duration }

func (s *recordingSleeper) Sleep(ctx context.Context, d time.Duration) error {
	s.delays = append(s.delays, d)
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return nil
}

func userRequest(t *testing.T) po.ModelRequest {
	t.Helper()
	user, err := po.NewUserTextMessage("u1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	req, err := po.NewModelRequest("", []po.Message{user}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRetryableProviderErrorRetries(t *testing.T) {
	base := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Fail(&provider.Error{Op: "test", StatusCode: 503, Retryable: true, Err: errors.New("temporary")}),
		scripted.Must(scripted.Reply("a1", "ok", po.Usage{})),
	)
	sleeper := &recordingSleeper{}
	model, err := retry.New(base, retry.Policy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: 4 * time.Second}, sleeper.Sleep)
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Generate(context.Background(), userRequest(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Message().Text() != "ok" {
		t.Fatalf("text = %q", response.Message().Text())
	}
	if base.CallCount() != 2 {
		t.Fatalf("calls = %d", base.CallCount())
	}
	if len(sleeper.delays) != 1 || sleeper.delays[0] != time.Second {
		t.Fatalf("delays = %v", sleeper.delays)
	}
}

func TestRetryAfterIsNotClamped(t *testing.T) {
	policy := retry.Policy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: 4 * time.Second}
	if got := policy.Delay(1, 30*time.Second); got != 30*time.Second {
		t.Fatalf("delay = %s", got)
	}
}

func TestNonRetryableErrorStopsImmediately(t *testing.T) {
	base := scripted.MustNew(scripted.DefaultInfo(), scripted.Fail(&provider.Error{
		Op: "test", StatusCode: 401, Retryable: false, Err: errors.New("bad key"),
	}))
	sleeper := &recordingSleeper{}
	model, err := retry.New(base, retry.Policy{MaxAttempts: 3, BaseDelay: time.Second}, sleeper.Sleep)
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(context.Background(), userRequest(t), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if base.CallCount() != 1 {
		t.Fatalf("calls = %d", base.CallCount())
	}
	if len(sleeper.delays) != 0 {
		t.Fatalf("delays = %v", sleeper.delays)
	}
}

type partialFailureModel struct{ calls int }

func (m *partialFailureModel) Info() po.ModelInfo { return scripted.DefaultInfo() }
func (m *partialFailureModel) Generate(ctx context.Context, req po.ModelRequest, emit po.DeltaEmitter) (po.ModelResponse, error) {
	m.calls++
	if emit != nil {
		if err := emit(ctx, po.ModelDelta{Kind: po.ModelDeltaText, ContentIndex: 0, Text: "partial"}); err != nil {
			return po.ModelResponse{}, err
		}
	}
	return po.ModelResponse{}, &provider.Error{Op: "test", StatusCode: 503, Retryable: true, Err: errors.New("stream broke")}
}

func TestDoesNotRetryAfterPartialStreaming(t *testing.T) {
	base := &partialFailureModel{}
	model, err := retry.New(base, retry.Policy{MaxAttempts: 3, BaseDelay: time.Millisecond}, (&recordingSleeper{}).Sleep)
	if err != nil {
		t.Fatal(err)
	}
	var deltas int
	_, err = model.Generate(
		context.Background(),
		userRequest(t),
		func(context.Context, po.ModelDelta) error { deltas++; return nil },
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if base.calls != 1 {
		t.Fatalf("calls = %d, want 1", base.calls)
	}
	if deltas != 1 {
		t.Fatalf("deltas = %d", deltas)
	}
}

func TestStopsAtMaxAttempts(t *testing.T) {
	base := scripted.MustNew(
		scripted.DefaultInfo(),
		scripted.Fail(&provider.Error{Op: "test", StatusCode: 503, Retryable: true, Err: errors.New("temporary-1")}),
		scripted.Fail(&provider.Error{Op: "test", StatusCode: 503, Retryable: true, Err: errors.New("temporary-2")}),
		scripted.Fail(&provider.Error{Op: "test", StatusCode: 503, Retryable: true, Err: errors.New("temporary-3")}),
	)
	sleeper := &recordingSleeper{}
	model, err := retry.New(base, retry.Policy{MaxAttempts: 3, BaseDelay: time.Second, JitterFraction: 0}, sleeper.Sleep)
	if err != nil {
		t.Fatal(err)
	}

	_, err = model.Generate(context.Background(), userRequest(t), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if base.CallCount() != 3 {
		t.Fatalf("calls = %d, want 3", base.CallCount())
	}
	if len(sleeper.delays) != 2 {
		t.Fatalf("sleep calls = %d, want 2", len(sleeper.delays))
	}
}
