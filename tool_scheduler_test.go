package po

import (
	"fmt"
	"testing"
)

func scheduledCall(index int, claims ...ResourceClaim) preparedToolCall {
	return preparedToolCall{
		batchIndex: index,
		call:       ToolCall{ID: fmt.Sprintf("call-%d", index)},
		claims:     claims,
	}
}

func nextScheduledIndex(t *testing.T, scheduler *toolBatchScheduler, want int) {
	t.Helper()
	call, ok := scheduler.startNext()
	if !ok {
		t.Fatalf("startNext() returned no call, want batch index %d", want)
	}
	if call.batchIndex != want {
		t.Fatalf("startNext() batch index = %d, want %d", call.batchIndex, want)
	}
}

func expectNoRunnableCall(t *testing.T, scheduler *toolBatchScheduler) {
	t.Helper()
	call, ok := scheduler.startNext()
	if ok {
		t.Fatalf("startNext() unexpectedly returned batch index %d", call.batchIndex)
	}
}

func completeScheduledCall(t *testing.T, scheduler *toolBatchScheduler, index int) {
	t.Helper()
	scheduler.complete(index)
}

// TestToolBatchSchedulerUsesResourceFIFO 覆盖调度器最关键的时序：不同文件可以并发，
// 同文件的独占访问等待前置共享访问，全局 Exclusive 又作为不可越过的 Workspace 屏障。
func TestToolBatchSchedulerUsesResourceFIFO(t *testing.T) {
	shared := ResourceAccessShared
	exclusive := ResourceAccessExclusive
	scheduler := newToolBatchScheduler([]preparedToolCall{
		scheduledCall(0, ResourceClaim{Key: "workspace", Mode: shared}, ResourceClaim{Key: "file:a.go", Mode: shared}),
		scheduledCall(1, ResourceClaim{Key: "workspace", Mode: shared}, ResourceClaim{Key: "file:b.go", Mode: shared}),
		scheduledCall(2, ResourceClaim{Key: "workspace", Mode: shared}, ResourceClaim{Key: "file:a.go", Mode: exclusive}),
		scheduledCall(3, ResourceClaim{Key: "workspace", Mode: exclusive}),
		scheduledCall(4, ResourceClaim{Key: "workspace", Mode: shared}, ResourceClaim{Key: "file:c.go", Mode: shared}),
	})

	nextScheduledIndex(t, scheduler, 0)
	nextScheduledIndex(t, scheduler, 1)
	expectNoRunnableCall(t, scheduler)

	completeScheduledCall(t, scheduler, 0)
	nextScheduledIndex(t, scheduler, 2)
	expectNoRunnableCall(t, scheduler)

	// edit(a.go) 可以和仍在执行的 read(b.go) 并发，但 Workspace Exclusive 必须等待两者。
	completeScheduledCall(t, scheduler, 2)
	expectNoRunnableCall(t, scheduler)
	completeScheduledCall(t, scheduler, 1)
	nextScheduledIndex(t, scheduler, 3)
	expectNoRunnableCall(t, scheduler)

	completeScheduledCall(t, scheduler, 3)
	nextScheduledIndex(t, scheduler, 4)
	completeScheduledCall(t, scheduler, 4)
}

func TestToolBatchSchedulerAcquiresMultipleResourcesWithoutDeadlock(t *testing.T) {
	scheduler := newToolBatchScheduler([]preparedToolCall{
		scheduledCall(0,
			ResourceClaim{Key: "a", Mode: ResourceAccessShared},
			ResourceClaim{Key: "b", Mode: ResourceAccessExclusive},
		),
		scheduledCall(1,
			ResourceClaim{Key: "a", Mode: ResourceAccessExclusive},
			ResourceClaim{Key: "b", Mode: ResourceAccessShared},
		),
	})

	nextScheduledIndex(t, scheduler, 0)
	expectNoRunnableCall(t, scheduler)
	completeScheduledCall(t, scheduler, 0)
	nextScheduledIndex(t, scheduler, 1)
	completeScheduledCall(t, scheduler, 1)
}

func TestNormalizeResourceClaimsMergesDuplicateKeyToExclusive(t *testing.T) {
	claims, err := normalizeResourceClaims([]ResourceClaim{
		{Key: " file:a.go ", Mode: ResourceAccessShared},
		{Key: "file:a.go", Mode: ResourceAccessExclusive},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0] != (ResourceClaim{Key: "file:a.go", Mode: ResourceAccessExclusive}) {
		t.Fatalf("normalized claims = %#v", claims)
	}
}

func TestNormalizeResourceClaimsRejectsInvalidClaim(t *testing.T) {
	tests := []ResourceClaim{
		{Key: "", Mode: ResourceAccessShared},
		{Key: "workspace", Mode: ResourceAccessMode(99)},
	}
	for _, claim := range tests {
		if _, err := normalizeResourceClaims([]ResourceClaim{claim}); err == nil {
			t.Fatalf("normalizeResourceClaims(%#v) unexpectedly succeeded", claim)
		}
	}
}
