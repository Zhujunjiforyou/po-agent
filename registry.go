package po

import (
	"fmt"
)

// ToolRegistry 是配置阶段可修改的工具集合
// 同时保存
// map：按模型返回的name快速查找tool
// order：保持注册顺序稳定，生成稳定toolsepc列表
type ToolRegistry struct {
	tools map[string]Tool
	order []string
}

// NewToolRegistry 创建空Registry
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// Register 注册一个工具。
func (r *ToolRegistry) Register(tool Tool) error {
	if tool == nil {
		return fmt.Errorf("%w: nil tool", ErrInvalidToolRegistry)
	}

	spec := tool.Spec()
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidToolRegistry, err)
	}

	if _, exists := r.tools[spec.Name()]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, spec.Name())
	}
	r.tools[spec.Name()] = tool
	r.order = append(r.order, spec.Name())

	return nil
}

// Lookup 按模型 ToolCall.Name 查找可执行工具。
func (r *ToolRegistry) Lookup(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// Specs 按稳定注册顺序返回模型可见的 ToolSpec。
func (r *ToolRegistry) Specs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(r.order))

	for _, name := range r.order {
		// Tool.Spec 本身返回 Clone，所以这里得到的是调用方可安全持有的快照。
		specs = append(specs, r.tools[name].Spec())
	}

	return specs
}

func (r *ToolRegistry) Len() int { return len(r.order) }
