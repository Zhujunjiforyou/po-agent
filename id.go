package po

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// IDGenerator 把“如何生成运行时 ID”从 Agent Loop 中抽离出来。
//
// 为什么需要这一层：
//   - 生产环境可以使用 UUID、ULID 或雪花 ID；
//   - 单元测试可以使用可预测的递增 ID；
//   - Agent Loop 不需要知道具体 ID 算法，只要求“每次给我一个稳定唯一值”。
//
// prefix 用来表达 ID 的用途，例如 message、tool-result、run、turn。
// 这对日志排查非常有帮助，因为只看字符串就能知道它属于哪种对象。
type IDGenerator interface {
	NewID(prefix string) string
}

// AtomicIDGenerator 是标准库实现的原子递增 ID 生成器。
// 零值实例保留简洁的进程内序号；NewAtomicIDGenerator 会加入实例命名空间，适合持久化 Session 恢复。
type AtomicIDGenerator struct {
	counter   atomic.Uint64
	namespace string
}

// NewAtomicIDGenerator 创建一个带实例命名空间的 ID 生成器。
func NewAtomicIDGenerator() *AtomicIDGenerator {
	return &AtomicIDGenerator{namespace: newIDNamespace()}
}

func newIDNamespace() string {
	var raw [8]byte
	if _, err := cryptorand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

// NewID 返回形如 "tool-result-1" 或 "tool-result-a1b2c3-1" 的 ID。
func (g *AtomicIDGenerator) NewID(prefix string) string {
	// TrimSpace 只是为了让默认实现的输出更整洁。
	// 真正的协议约束仍由 Message.Validate 负责，而不是把所有校验塞进生成器。
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "id"
	}

	// atomic.Uint64.Add 在多个 goroutine 同时调用时不会产生数据竞争。
	// 后面 Agent 支持多个 Run 并发执行时，这一点会直接派上用场。
	n := g.counter.Add(1)
	if g.namespace != "" {
		return fmt.Sprintf("%s-%s-%d", prefix, g.namespace, n)
	}
	return fmt.Sprintf("%s-%d", prefix, n)
}

var _ IDGenerator = (*AtomicIDGenerator)(nil)
