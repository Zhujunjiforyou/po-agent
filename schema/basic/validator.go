// Package basic 提供 Po 示例和测试使用的基础 JSON Schema 子集校验器。
package basic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

// Validator 是一个无状态 Schema Validator。
//
// 它不缓存编译结果；需要高吞吐的调用方可以在外层按 Schema 哈希增加缓存。
type Validator struct{}

// New 返回一个基础 Validator。
func New() *Validator { return &Validator{} }

// ValidationError 是“instance 不满足 schema”时的结构化错误。
//
// Path 告诉模型和开发者具体哪个值有问题；
// Keyword 说明违反的是 type、required、minimum 等哪条规则；
// Message 保存适合人类阅读的解释。
type ValidationError struct {
	Path    string
	Keyword string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Keyword == "" {
		return fmt.Sprintf("%s: %s", e.Path, e.Message)
	}

	return fmt.Sprintf("%s: %s (%s)", e.Path, e.Message, e.Keyword)
}

// Validate 是外部调用入口。
func (v *Validator) Validate(schemaRaw json.RawMessage, instanceRaw json.RawMessage) error {
	// Schema 自己也是不可信配置数据，先完整解析并要求根节点是 object。
	schemaValue, err := decodeOne(schemaRaw)
	if err != nil {
		return fmt.Errorf("invalid schema JSON: %w", err)
	}

	schema, ok := schemaValue.(map[string]any)
	if !ok {
		return fmt.Errorf("schema root must be an object")
	}

	// 参数 JSON 与 Schema 分开解析。
	// 两边都使用 UseNumber，避免数字提前丢失精度。
	instance, err := decodeOne(instanceRaw)
	if err != nil {
		return fmt.Errorf("invalid instance JSON: %w", err)
	}

	// `$` 表示 JSON 文档根节点。
	return validateNode(schema, instance, "$")
}

// decodeOne 只接受“恰好一个”JSON 值，并保留数字为 json.Number。
func decodeOne(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}

	// 第二次 Decode 必须得到 EOF，拒绝 `{} {}` 这种拼接输入。
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected exactly one JSON value")
	}

	return value, nil
}

// validateNode 是整个 Validator 的递归核心。
//
// 每进入一个子对象、数组元素或属性，就带着新的 Path 继续递归。
// 因而失败时天然知道问题发生在哪里，而不需要事后重新遍历寻找路径。
func validateNode(schema map[string]any, value any, path string) error {
	// enum 与具体 type 无关，所以先处理。
	if rawEnum, exists := schema["enum"]; exists {
		enumValues, ok := rawEnum.([]any)
		if !ok || len(enumValues) == 0 {
			return fmt.Errorf("schema at %s: enum must be a non-empty array", path)
		}

		matched := false
		for _, candidate := range enumValues {
			// Schema 和 Instance 都由同一 decodeOne + UseNumber 解码，
			// 所以 reflect.DeepEqual 在当前子集中能保持稳定 JSON 值语义。
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}

		if !matched {
			return &ValidationError{
				Path:    path,
				Keyword: "enum",
				Message: "value is not one of the allowed values",
			}
		}
	}

	// 当前子集要求 type 是单个字符串；完整 JSON Schema 还允许更复杂形式。
	typeName, _ := schema["type"].(string)

	if typeName != "" {
		if err := checkType(typeName, value, path); err != nil {
			return err
		}
	}

	// 先由 checkType 保证具体 Go 类型，下面的类型断言才是安全的。
	switch typeName {
	case "object":
		return validateObject(schema, value.(map[string]any), path)
	case "array":
		return validateArray(schema, value.([]any), path)
	case "string":
		return validateString(schema, value.(string), path)
	case "number":
		return validateNumber(schema, value.(json.Number), path, false)
	case "integer":
		return validateNumber(schema, value.(json.Number), path, true)
	case "boolean", "null", "":
		// 当前子集对 boolean/null 没有额外关键词。
		// type 为空时，只执行 enum 或嵌套规则。
		return nil
	default:
		return fmt.Errorf("schema at %s: unsupported type %q", path, typeName)
	}
}

// checkType 把 JSON 世界的 type 映射到 decodeOne 后的 Go 动态类型。
func checkType(want string, value any, path string) error {
	matched := false

	switch want {
	case "object":
		_, matched = value.(map[string]any)
	case "array":
		_, matched = value.([]any)
	case "string":
		_, matched = value.(string)
	case "number":
		_, matched = value.(json.Number)
	case "integer":
		if number, ok := value.(json.Number); ok {
			matched = isInteger(number)
		}
	case "boolean":
		_, matched = value.(bool)
	case "null":
		matched = value == nil
	default:
		return fmt.Errorf("schema at %s: unsupported type %q", path, want)
	}

	if !matched {
		return &ValidationError{
			Path:    path,
			Keyword: "type",
			Message: fmt.Sprintf("expected %s", want),
		}
	}

	return nil
}

// validateObject 处理 properties、required 与 additionalProperties。
func validateObject(schema map[string]any, object map[string]any, path string) error {
	// 先解析 required，并把它转换成 set，方便 O(1) 检查。
	required := make(map[string]bool)

	if rawRequired, exists := schema["required"]; exists {
		items, ok := rawRequired.([]any)
		if !ok {
			return fmt.Errorf("schema at %s: required must be an array", path)
		}

		for _, item := range items {
			name, ok := item.(string)
			if !ok {
				return fmt.Errorf("schema at %s: required items must be strings", path)
			}
			required[name] = true
		}
	}

	// required 错误指向缺失属性的位置，例如 $.path。
	for name := range required {
		if _, exists := object[name]; !exists {
			return &ValidationError{
				Path:    joinPath(path, name),
				Keyword: "required",
				Message: "required property is missing",
			}
		}
	}

	properties := make(map[string]any)

	if rawProperties, exists := schema["properties"]; exists {
		parsed, ok := rawProperties.(map[string]any)
		if !ok {
			return fmt.Errorf("schema at %s: properties must be an object", path)
		}
		properties = parsed
	}

	// JSON Schema 的默认语义允许额外属性。
	// Tool Calling 中通常显式设置 false，但 Validator 仍尊重默认 true。
	allowAdditional := true

	if rawAdditional, exists := schema["additionalProperties"]; exists {
		allowed, ok := rawAdditional.(bool)
		if !ok {
			// 完整规范允许 additionalProperties 是一个 Schema；
			// 当前子集只实现 boolean，遇到其他形式立即明确失败，而不是静默忽略。
			return fmt.Errorf("schema at %s: only boolean additionalProperties is supported", path)
		}
		allowAdditional = allowed
	}

	// Go map 遍历顺序不稳定。
	// 为使多个错误候选中的“第一个错误”可预测，先排序属性名。
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, name := range keys {
		value := object[name]
		rawChildSchema, known := properties[name]

		if !known {
			if !allowAdditional {
				return &ValidationError{
					Path:    joinPath(path, name),
					Keyword: "additionalProperties",
					Message: "unknown property is not allowed",
				}
			}
			continue
		}

		childSchema, ok := rawChildSchema.(map[string]any)
		if !ok {
			return fmt.Errorf("schema at %s: property %q schema must be object", path, name)
		}

		if err := validateNode(childSchema, value, joinPath(path, name)); err != nil {
			return err
		}
	}

	return nil
}

// validateArray 把同一个 items Schema 递归应用到每一个数组元素。
func validateArray(schema map[string]any, array []any, path string) error {
	rawItems, exists := schema["items"]
	if !exists {
		return nil
	}

	itemSchema, ok := rawItems.(map[string]any)
	if !ok {
		return fmt.Errorf("schema at %s: items must be an object", path)
	}

	for index, item := range array {
		// 数组路径使用 [index]，因此错误会得到 $.files[1].path。
		itemPath := fmt.Sprintf("%s[%d]", path, index)

		if err := validateNode(itemSchema, item, itemPath); err != nil {
			return err
		}
	}

	return nil
}

// validateString 使用 Rune 数，而不是 UTF-8 byte 数检查长度。
func validateString(schema map[string]any, value string, path string) error {
	length := utf8.RuneCountInString(value)

	if rawMinimum, exists := schema["minLength"]; exists {
		minimum, err := schemaInt(rawMinimum)
		if err != nil {
			return fmt.Errorf("schema at %s: minLength: %w", path, err)
		}

		if length < minimum {
			return &ValidationError{
				Path:    path,
				Keyword: "minLength",
				Message: fmt.Sprintf("length %d is less than %d", length, minimum),
			}
		}
	}

	if rawMaximum, exists := schema["maxLength"]; exists {
		maximum, err := schemaInt(rawMaximum)
		if err != nil {
			return fmt.Errorf("schema at %s: maxLength: %w", path, err)
		}

		if length > maximum {
			return &ValidationError{
				Path:    path,
				Keyword: "maxLength",
				Message: fmt.Sprintf("length %d exceeds %d", length, maximum),
			}
		}
	}

	return nil
}

// validateNumber 使用 big.Rat 做精确有理数比较。
func validateNumber(schema map[string]any, number json.Number, path string, requireInteger bool) error {
	if requireInteger && !isInteger(number) {
		return &ValidationError{
			Path:    path,
			Keyword: "type",
			Message: "expected integer",
		}
	}

	value, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return &ValidationError{
			Path:    path,
			Keyword: "type",
			Message: "invalid JSON number",
		}
	}

	if rawMinimum, exists := schema["minimum"]; exists {
		minimum, err := schemaRat(rawMinimum)
		if err != nil {
			return fmt.Errorf("schema at %s: minimum: %w", path, err)
		}

		if value.Cmp(minimum) < 0 {
			return &ValidationError{
				Path:    path,
				Keyword: "minimum",
				Message: fmt.Sprintf("value %s is less than %s", number, minimum.RatString()),
			}
		}
	}

	if rawMaximum, exists := schema["maximum"]; exists {
		maximum, err := schemaRat(rawMaximum)
		if err != nil {
			return fmt.Errorf("schema at %s: maximum: %w", path, err)
		}

		if value.Cmp(maximum) > 0 {
			return &ValidationError{
				Path:    path,
				Keyword: "maximum",
				Message: fmt.Sprintf("value %s exceeds %s", number, maximum.RatString()),
			}
		}
	}

	return nil
}

// isInteger 判断 JSON Number 是否数学意义上为整数。
func isInteger(number json.Number) bool {
	value, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return false
	}

	return value.Denom().Cmp(big.NewInt(1)) == 0
}

// schemaRat 把 Schema 中的 minimum/maximum 精确解析成有理数。
func schemaRat(value any) (*big.Rat, error) {
	number, ok := value.(json.Number)
	if !ok {
		return nil, fmt.Errorf("must be a number")
	}

	result, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return nil, fmt.Errorf("invalid number")
	}

	return result, nil
}

// schemaInt 解析 minLength/maxLength 这类非负整数 Schema 参数。
func schemaInt(value any) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("must be an integer")
	}

	integer, err := number.Int64()
	if err != nil || integer < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}

	return int(integer), nil
}

// joinPath 为普通属性生成 $.foo.bar；
// 对包含点号、方括号等特殊字符的属性退化为 $["strange.name"]。
func joinPath(path, name string) string {
	if strings.ContainsAny(name, ".[]") {
		return path + "[" + fmt.Sprintf("%q", name) + "]"
	}

	return path + "." + name
}
