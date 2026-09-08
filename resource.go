package po

import (
	"fmt"
	"strings"
)

// ResourceAccessMode 描述一个 ToolCall 对逻辑资源的共享或独占访问需求。
// 它只用于 Runtime 调度，不会发送给模型。
type ResourceAccessMode uint8

const (
	ResourceAccessShared ResourceAccessMode = iota + 1
	ResourceAccessExclusive
)

func (m ResourceAccessMode) validate() error {
	switch m {
	case ResourceAccessShared, ResourceAccessExclusive:
		return nil
	default:
		return fmt.Errorf("invalid resource access mode %d", m)
	}
}

// ResourceClaim 表示一个 ToolCall 执行期间需要持有的逻辑资源。
// 相同 Key 的 Shared Claim 可以并存，Exclusive Claim 必须独占该资源。
type ResourceClaim struct {
	Key  string
	Mode ResourceAccessMode
}

// ResourceResolver 根据已经通过协议和 Schema 校验的 ToolCall 推导资源需求。
// ToolCall 参数来自模型，因此 Resolver 仍然必须规范化路径等资源标识。
// Resolver 只协调同一条 AssistantMessage 中的批次，不是跨 Run 或跨进程锁。
// 同一个 Agent 可以并发运行多个 Run，因此实现不能修改未受保护的共享状态。
type ResourceResolver func(ToolCall) ([]ResourceClaim, error)

func normalizeResourceClaims(claims []ResourceClaim) ([]ResourceClaim, error) {
	if len(claims) == 0 {
		return nil, nil
	}

	normalized := make([]ResourceClaim, 0, len(claims))
	byKey := make(map[string]int, len(claims))
	for index, claim := range claims {
		claim.Key = strings.TrimSpace(claim.Key)
		if claim.Key == "" {
			return nil, fmt.Errorf("resource claim %d has an empty key", index)
		}
		if err := claim.Mode.validate(); err != nil {
			return nil, fmt.Errorf("resource claim %q: %w", claim.Key, err)
		}

		if existing, ok := byKey[claim.Key]; ok {
			if claim.Mode == ResourceAccessExclusive {
				normalized[existing].Mode = ResourceAccessExclusive
			}
			continue
		}
		byKey[claim.Key] = len(normalized)
		normalized = append(normalized, claim)
	}
	return normalized, nil
}
