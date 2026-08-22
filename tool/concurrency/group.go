// Package concurrency 提供 Tool 层可选的资源并发限制。
package concurrency

import (
	"context"
	"fmt"
	po "github.com/lemonzjj/po-agent-go"
	"reflect"
)

// Group 是一个被多个Tool共享的counting semaphore
// limit=1 相当于这组资源互斥；limit=4 表示最多四个调用同时占用它。Group 故意不
// 实现优先级、公平队列、weighted resource、任务持久化等能力，因为一旦需要这些
// 语义，此时已经不是“小型并发上限”，而是一个真正的 Scheduler。

type Group struct {
	// tokens 是固定容量的buffered channel，每写入一个struct{}{} 就表示当前占用一个slot
	tokens chan struct{}
}

func NewGroup(limit int) (*Group, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("concurrency group limit must be freater than zero")
	}
	return &Group{tokens: make(chan struct{}, limit)}, nil
}

// MustNewGroup 适合静态应用组装和测试；如果 limit 来自用户配置，应使用 NewGroup
// 并正常返回错误，而不是因为配置错误让进程 panic。
func MustNewGroup(limit int) *Group {
	group, err := NewGroup(limit)
	if err != nil {
		panic(err)
	}
	return group
}

// acquire 等待一个资源 slot。如果等待期间 Run 被取消，就立即返回 Context Cause。
//
// 返回 release closure 的目的，是把“获得资源”和“归还同一个资源”绑定到一次调用的所有权上。
// 只有成功 acquire 的调用者才会拿到 release，也只应该调用一次。
func (g *Group) acquire(ctx context.Context) (release func(), err error) {
	if g == nil {
		return nil, fmt.Errorf("concurrency group is nil")
	}

	select {
	case g.tokens <- struct{}{}:
		return func() { <-g.tokens }, nil
	case <-ctx.Done():
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		return nil, ctx.Err()
	}
}

// Tool 是透明的 po.Tool decorator。多个不同 Tool wrapper 可以共享一个 Group，
// 这就是应用层表达“这些 Tool 实际竞争同一类资源”的方式。
type Tool struct {
	base  po.Tool
	group *Group
}

// Wrap 给一个 Tool 增加资源并发限制，但不改变模型可见 ToolSpec。
func Wrap(base po.Tool, group *Group) (*Tool, error) {
	if isNilTool(base) {
		return nil, fmt.Errorf("concurrency wrapper requires a base tool")
	}
	if group == nil {
		return nil, fmt.Errorf("concurrency wrapper requires a group")
	}
	return &Tool{base: base, group: group}, nil
}

// Spec 直接透传底层 Tool。资源调度是 Runtime 机制，不应该偷偷改变模型看到的
// name、description 或 JSON Schema。
func (t *Tool) Spec() po.ToolSpec { return t.base.Spec() }

// Execute 先获得共享资源 slot，再调用底层 Tool。
//
// defer release() 在这里非常自然：slot 的生命周期恰好属于这一层 Execute 调用，
// 无论底层 Tool 正常返回还是返回 error，栈展开都会释放它。底层 panic 会继续向外
// 传播，并由 Agent Core 的 Tool panic boundary 统一转成 error；defer 仍然会执行。
func (t *Tool) Execute(ctx context.Context, call po.ToolCall, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
	release, err := t.group.acquire(ctx)
	if err != nil {
		return po.ToolResult{}, err
	}
	defer release()

	return t.base.Execute(ctx, call, emit)
}

func isNilTool(tool po.Tool) bool {
	if tool == nil {
		return true
	}

	value := reflect.ValueOf(tool)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ po.Tool = (*Tool)(nil)
