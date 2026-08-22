package po

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestEventHubRegistrationOrder(t *testing.T) {
	var hub eventHub
	got := make([]string, 0, 2)
	hub.subscribe(func(ctx context.Context, event AgentEvent) {
		got = append(got, "first")
	})
	hub.subscribe(func(ctx context.Context, event AgentEvent) {
		got = append(got, "second")
	})
	hub.emit(context.Background(), RunStartEvent{RunID: "run-1"})
	want := []string{"first", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEventHubUnsubscribe(t *testing.T) {
	var hub eventHub
	calls := 0
	unsubscribe := hub.subscribe(func(ctx context.Context, event AgentEvent) {
		calls++
	})
	hub.emit(context.Background(), RunStartEvent{})
	unsubscribe()

	// 重复取消应该是幂等的。
	unsubscribe()
	hub.emit(context.Background(), RunEndEvent{})
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestHandlerCanUnsubscribeItself(t *testing.T) {
	var hub eventHub
	calls := 0
	var unsubscribe func()
	unsubscribe = hub.subscribe(func(ctx context.Context, event AgentEvent) {
		calls++
		unsubscribe()
	})
	hub.emit(context.Background(), RunStartEvent{})
	hub.emit(context.Background(), RunEndEvent{})
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestEventHubSerializesConcurrentEmit(t *testing.T) {
	var hub eventHub
	var stateMu sync.Mutex
	active := 0
	maxActive := 0
	hub.subscribe(func(ctx context.Context, event AgentEvent) {
		stateMu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		stateMu.Unlock()

		// 故意延长 Callback，
		// 增加多个 goroutine Event Delivery 发生重叠的机会。
		time.Sleep(time.Millisecond)
		stateMu.Lock()
		active--
		stateMu.Unlock()
	})
	var waitGroup sync.WaitGroup
	for index := 0; index < 32; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			hub.emit(context.Background(), RunStartEvent{})
		}()
	}
	waitGroup.Wait()
	if maxActive != 1 {
		t.Fatalf("handler ran concurrently: max=%d", maxActive)
	}
}

func TestEventHubUsesSnapshotDuringDelivery(t *testing.T) {
	var hub eventHub
	var order []string
	var unsubscribeSecond func()
	hub.subscribe(func(ctx context.Context, event AgentEvent) {
		order = append(order, "first")

		// 当前 Event 已经取得 subscriber snapshot。
		// 所以 second 仍应收到当前 Event，
		// 但不会收到下一条 Event。
		unsubscribeSecond()
	})
	unsubscribeSecond = hub.subscribe(func(ctx context.Context, event AgentEvent) {
		order = append(order, "second")
	})
	hub.emit(context.Background(), RunStartEvent{})
	hub.emit(context.Background(), RunEndEvent{})
	want := []string{"first", "second", "first"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestEventTypes(t *testing.T) {
	tests := []struct {
		event AgentEvent
		want  AgentEventType
	}{
		{event: RunStartEvent{}, want: EventRunStart},
		{event: RunEndEvent{}, want: EventRunEnd},
		{event: TurnStartEvent{}, want: EventTurnStart},
		{event: TurnEndEvent{}, want: EventTurnEnd},
		{event: MessageStartEvent{}, want: EventMessageStart},
		{event: MessageUpdateEvent{}, want: EventMessageUpdate},
		{event: MessageEndEvent{}, want: EventMessageEnd},
		{event: ToolStartEvent{}, want: EventToolStart},
		{event: ToolUpdateEvent{}, want: EventToolUpdate},
		{event: ToolEndEvent{}, want: EventToolEnd},
	}

	for _, test := range tests {
		if got := test.event.Type(); got != test.want {
			t.Fatalf("event %T type = %q, want %q", test.event, got, test.want)
		}
	}
}
