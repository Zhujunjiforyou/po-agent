package po

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// AtomicIDGenerator 生成带进程内命名空间的递增 ID。
type AtomicIDGenerator struct {
	counter   atomic.Uint64
	namespace string
}

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

func (g *AtomicIDGenerator) NewID(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "id"
	}

	n := g.counter.Add(1)
	if g.namespace != "" {
		return fmt.Sprintf("%s-%s-%d", prefix, g.namespace, n)
	}
	return fmt.Sprintf("%s-%d", prefix, n)
}
