package concurrency

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	po "github.com/lemonzjj/po-agent-go"
)

type blockingTool struct {
	spec po.ToolSpec

	active    *atomic.Int32
	maxActive *atomic.Int32
	release   <-chan struct{}
}

func (t *blockingTool) Spec() po.ToolSpec { return t.spec.Clone() }

func (t *blockingTool) Execute(ctx context.Context, _ po.ToolCall, _ po.ToolUpdateEmitter) (po.ToolResult, error) {
	active := t.active.Add(1)
	defer t.active.Add(-1)

	for {
		max := t.maxActive.Load()
		if active <= max || t.maxActive.CompareAndSwap(max, active) {
			break
		}
	}

	select {
	case <-t.release:
		return po.NewTextToolResult("done", nil, false)
	case <-ctx.Done():
		return po.ToolResult{}, ctx.Err()
	}
}

func mustSpec(t *testing.T, name string) po.ToolSpec {
	t.Helper()
	spec, err := po.NewToolSpec(name, "test tool", []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatalf("NewToolSpec() error = %v", err)
	}
	return spec
}

func mustCall(t *testing.T, id, name string) po.ToolCall {
	t.Helper()
	call, err := po.NewToolCall(id, name, struct{}{})
	if err != nil {
		t.Fatalf("NewToolCall() error = %v", err)
	}
	return call
}

func TestSharedGroupLimitsDifferentTools(t *testing.T) {
	group := MustNewGroup(1)
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32

	first, err := Wrap(&blockingTool{spec: mustSpec(t, "first"), active: &active, maxActive: &maxActive, release: release}, group)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Wrap(&blockingTool{spec: mustSpec(t, "second"), active: &active, maxActive: &maxActive, release: release}, group)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = first.Execute(context.Background(), mustCall(t, "call-1", "first"), nil)
	}()
	go func() {
		defer wg.Done()
		_, _ = second.Execute(context.Background(), mustCall(t, "call-2", "second"), nil)
	}()

	// 给两个协程留出竞争同一分组的时间。在释放首个持有者前，只能有一个底层 Execute
	// 进入临界资源区域。
	time.Sleep(20 * time.Millisecond)
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent base executions = %d, want 1", got)
	}

	close(release)
	wg.Wait()
}

func TestWaitingForGroupRespectsContextCancellation(t *testing.T) {
	group := MustNewGroup(1)

	// 通过 acquire 直接占用唯一名额，确保被包装的 Tool 必须等待。
	release, err := group.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	base := &blockingTool{
		spec:      mustSpec(t, "blocked"),
		active:    &atomic.Int32{},
		maxActive: &atomic.Int32{},
		release:   make(chan struct{}),
	}
	wrapped, err := Wrap(base, group)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = wrapped.Execute(ctx, mustCall(t, "call-1", "blocked"), nil)
	if err == nil {
		t.Fatal("expected canceled wait to fail")
	}
}
