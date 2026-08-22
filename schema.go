package po

import (
	"encoding/json"
	"fmt"
)

// SchemaValidator 是Agent core对参数验证器的最小要求
type SchemaValidator interface {
	Validate(schema json.RawMessage, instance json.RawMessage) error
}

// ValidateToolCallArguments 把ToolSpec、ToolCall 和 SchemaValidator连在一起
func ValidateToolCallArguments(validator SchemaValidator, spec ToolSpec, call ToolCall) error {
	if isNilInterface(validator) {
		return fmt.Errorf("%w: validator is required", ErrToolArgumentValidation)
	}
	// 先检查静态 ToolSpec；Schema 自己非法属于开发配置错误。
	if err := spec.Validate(); err != nil {
		return err
	}

	// 再检查模型发来的 ToolCall 协议外形。
	if err := call.Validate(); err != nil {
		return err
	}

	// 正常情况下 Registry 已经按 call.Name 找到了 spec 对应 Tool。
	// 再检查一次可以避免上层把错误的 Spec 和 Call 组合进来。
	if call.Name != spec.Name() {
		return fmt.Errorf(
			"%w: call targets %q but spec is %q",
			ErrToolArgumentValidation,
			call.Name,
			spec.Name(),
		)
	}

	// Schema 的来源和 Instance 的来源非常清楚：
	//   Schema   → 开发者定义的 ToolSpec
	//   Instance → 模型产生的 ToolCall.Arguments
	if err := validator.Validate(spec.InputSchema(), call.Arguments); err != nil {
		return fmt.Errorf("%w: %v", ErrToolArgumentValidation, err)
	}

	return nil
}
