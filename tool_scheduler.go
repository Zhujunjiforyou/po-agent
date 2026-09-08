package po

import "container/heap"

type scheduledToolCall struct {
	prepared   preparedToolCall
	ready      int
	claimCount int
}

type resourceQueueEntry struct {
	call *scheduledToolCall
	mode ResourceAccessMode
}

// resourceQueue 按 AssistantMessage 中的 source order 保存访问同一逻辑资源的
// ToolCall。队头连续的 Shared 可以一起获得准入；Exclusive 只有成为队头后才能准入。
type resourceQueue struct {
	entries  []resourceQueueEntry
	next     int
	reserved int
}

func (q *resourceQueue) reserveNext(scheduler *toolBatchScheduler) {
	if q.reserved != 0 || q.next == len(q.entries) {
		return
	}

	if q.entries[q.next].mode == ResourceAccessExclusive {
		q.reserve(q.entries[q.next], scheduler)
		q.next++
		return
	}
	for q.next < len(q.entries) && q.entries[q.next].mode == ResourceAccessShared {
		q.reserve(q.entries[q.next], scheduler)
		q.next++
	}
}

func (q *resourceQueue) reserve(entry resourceQueueEntry, scheduler *toolBatchScheduler) {
	q.reserved++
	entry.call.ready++
	if entry.call.ready == entry.call.claimCount {
		heap.Push(&scheduler.runnable, entry.call)
	}
}

func (q *resourceQueue) release(scheduler *toolBatchScheduler) {
	q.reserved--
	if q.reserved == 0 {
		q.reserveNext(scheduler)
	}
}

// toolBatchScheduler 是单批 ToolCall 的中央准入器。所有状态都由 Agent 的批次
// coordinator 修改，worker 只通过 completion channel 返回结果，因此这里不需要锁。
type toolBatchScheduler struct {
	calls        []scheduledToolCall
	byBatchIndex map[int]*scheduledToolCall
	resources    map[string]*resourceQueue
	runnable     runnableCallHeap
	running      int
}

func newToolBatchScheduler(prepared []preparedToolCall) *toolBatchScheduler {
	scheduler := &toolBatchScheduler{
		calls:        make([]scheduledToolCall, len(prepared)),
		byBatchIndex: make(map[int]*scheduledToolCall, len(prepared)),
		resources:    make(map[string]*resourceQueue),
	}

	for index, item := range prepared {
		current := &scheduler.calls[index]
		current.prepared = item
		current.claimCount = len(item.claims)
		scheduler.byBatchIndex[item.batchIndex] = current

		for _, claim := range item.claims {
			queue := scheduler.resources[claim.Key]
			if queue == nil {
				queue = &resourceQueue{}
				scheduler.resources[claim.Key] = queue
			}
			queue.entries = append(queue.entries, resourceQueueEntry{call: current, mode: claim.Mode})
		}
	}

	for index := range scheduler.calls {
		if scheduler.calls[index].claimCount == 0 {
			heap.Push(&scheduler.runnable, &scheduler.calls[index])
		}
	}
	for _, queue := range scheduler.resources {
		queue.reserveNext(scheduler)
	}
	return scheduler
}

func (s *toolBatchScheduler) startNext() (preparedToolCall, bool) {
	if len(s.runnable) == 0 {
		return preparedToolCall{}, false
	}
	call := heap.Pop(&s.runnable).(*scheduledToolCall)
	s.running++
	return call.prepared, true
}

func (s *toolBatchScheduler) complete(batchIndex int) {
	call := s.byBatchIndex[batchIndex]
	s.running--
	for _, claim := range call.prepared.claims {
		s.resources[claim.Key].release(s)
	}
}

func (s *toolBatchScheduler) runningCount() int { return s.running }

type runnableCallHeap []*scheduledToolCall

func (h runnableCallHeap) Len() int { return len(h) }
func (h runnableCallHeap) Less(left, right int) bool {
	return h[left].prepared.batchIndex < h[right].prepared.batchIndex
}
func (h runnableCallHeap) Swap(left, right int) { h[left], h[right] = h[right], h[left] }
func (h *runnableCallHeap) Push(value any)      { *h = append(*h, value.(*scheduledToolCall)) }
func (h *runnableCallHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	old[last] = nil
	*h = old[:last]
	return value
}
