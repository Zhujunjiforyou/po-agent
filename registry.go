package po

import (
	"fmt"
	"sync"
)

// ToolRegistry 是配置阶段可修改的工具集合
// 同时保存
// map：按模型返回的name快速查找tool
// order：保持注册顺序稳定，生成稳定toolsepc列表
type ToolRegistry struct {
	mu    sync.RWMutex
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
	// 接口值存在的go陷阱：
	// var p *MyTool = nil
	// var t Tool = p
	// 此时 t != nil，因为接口内部保存了动态类型*MyTool
	if isNilInterface(tool) {
		return fmt.Errorf("%w: nil tool", ErrInvalidToolRegistry)
	}

	spec := tool.Spec()
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidToolRegistry, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 不允许静默覆盖
	if _, exists := r.tools[spec.Name()]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, spec.Name())
	}
	r.tools[spec.Name()] = tool
	r.order = append(r.order, spec.Name())

	return nil
}

// Lookup 按模型 ToolCall.Name 查找可执行工具。
func (r *ToolRegistry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.tools[name]
	return tool, ok
}

// Specs 按稳定注册顺序返回模型可见的 ToolSpec。
func (r *ToolRegistry) Specs() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()

	specs := make([]ToolSpec, 0, len(r.order))

	for _, name := range r.order {
		// Tool.Spec 本身返回 Clone，所以这里得到的是调用方可安全持有的快照。
		specs = append(specs, r.tools[name].Spec())
	}

	return specs
}

// ToolSnapshot 是一个 Run 使用的稳定 Registry 视图。
// 字段不导出，因此外部只能 Lookup/Specs，不能修改内部 map。
type ToolSnapshot struct {
	tools map[string]Tool
	order []string
}

// Snapshot 创建一个供单次 Run 使用的稳定工具集合。
//
// Snapshot 之后即使 Registry 再 Register，新 Run 仍会读取最新工具集合，
// 已经启动的旧 Run 仍使用自己的快照，不会在中途改变模型能力。
func (r *ToolRegistry) Snapshot() ToolSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make(map[string]Tool, len(r.tools))
	for name, tool := range r.tools {
		// 复制 map 关系，但不试图深拷贝任意 Tool 实例。
		tools[name] = tool
	}

	return ToolSnapshot{
		tools: tools,
		order: append([]string(nil), r.order...),
	}
}

// Lookup 在固定快照中查找工具。
func (s ToolSnapshot) Lookup(name string) (Tool, bool) {
	tool, ok := s.tools[name]
	return tool, ok
}

// Specs 返回固定快照中的模型可见工具列表。
func (s ToolSnapshot) Specs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(s.order))

	for _, name := range s.order {
		specs = append(specs, s.tools[name].Spec())
	}

	return specs
}

// Len 主要用于测试、日志和快速判断当前 Run 是否有工具能力。
func (s ToolSnapshot) Len() int {
	return len(s.order)
}
