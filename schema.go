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
	if validator == nil {
		return fmt.Errorf("%w: validator is required", ErrToolArgumentValidation)
	}
	if err := validator.Validate(spec.InputSchema(), call.Arguments); err != nil {
		return fmt.Errorf("%w: %v", ErrToolArgumentValidation, err)
	}

	return nil
}
