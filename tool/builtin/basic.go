// Package builtin 为 Po 应用提供小型通用工具。
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	po "github.com/lemonzjj/po-agent-go"
)

// EchoArgs 是 echo 工具的强类型参数。
type EchoArgs struct {
	Message string `json:"message"`
}

// NewEcho 创建一个最小但完整的强类型工具。
func NewEcho() po.Tool {
	spec := mustSpec(
		"echo",
		"原样返回给定文本。用于验证工具调用、参数解码和 Tool Result 回填。",
		`{
			"type":"object",
			"properties":{
				"message":{"type":"string","minLength":1}
			},
			"required":["message"],
			"additionalProperties":false
		}`,
	)

	return po.MustNewTypedTool(
		spec,
		func(args EchoArgs) error {
			if strings.TrimSpace(args.Message) == "" {
				return errors.New("message cannot be blank")
			}
			return nil
		},
		func(ctx context.Context, args EchoArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return po.ToolResult{}, err
			}
			details := map[string]any{"character_count": len([]rune(args.Message))}
			return po.NewTextToolResult(args.Message, details, false)
		},
	)
}

// CalculatorArgs 是 calculator 工具的输入参数。
type CalculatorArgs struct {
	Operation string  `json:"operation"`
	A         float64 `json:"a"`
	B         float64 `json:"b"`
}

// NewCalculator 创建四则运算工具。
func NewCalculator() po.Tool {
	spec := mustSpec(
		"calculator",
		"执行加、减、乘、除四则运算。operation 只能是 add、subtract、multiply 或 divide。",
		`{
			"type":"object",
			"properties":{
				"operation":{"type":"string","enum":["add","subtract","multiply","divide"]},
				"a":{"type":"number"},
				"b":{"type":"number"}
			},
			"required":["operation","a","b"],
			"additionalProperties":false
		}`,
	)

	return po.MustNewTypedTool(
		spec,
		func(args CalculatorArgs) error {
			switch args.Operation {
			case "add", "subtract", "multiply":
				return nil
			case "divide":
				if args.B == 0 {
					return errors.New("division by zero is not allowed")
				}
				return nil
			default:
				return fmt.Errorf("unsupported operation %q", args.Operation)
			}
		},
		func(ctx context.Context, args CalculatorArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return po.ToolResult{}, err
			}

			var value float64
			switch args.Operation {
			case "add":
				value = args.A + args.B
			case "subtract":
				value = args.A - args.B
			case "multiply":
				value = args.A * args.B
			case "divide":
				value = args.A / args.B
			}

			return po.NewTextToolResult(fmt.Sprintf("计算结果：%g", value), map[string]any{
				"operation": args.Operation,
				"a":         args.A,
				"b":         args.B,
				"result":    value,
			}, false)
		},
	)
}

// CurrentDirectoryArgs 明确表示工具参数是一个空 JSON object。
type CurrentDirectoryArgs struct{}

// NewCurrentDirectory 创建返回当前进程工作目录的工具。
func NewCurrentDirectory() po.Tool { return NewCurrentDirectoryWith(os.Getwd) }

// NewCurrentDirectoryWith 允许测试注入 getwd，实现不依赖真实进程目录的确定性测试。
func NewCurrentDirectoryWith(getwd func() (string, error)) po.Tool {
	if getwd == nil {
		panic("getwd is required")
	}

	spec := mustSpec(
		"current_directory",
		"返回 Po Agent 进程当前使用的工作目录。这个工具不接收任何参数。",
		`{
			"type":"object",
			"properties":{},
			"additionalProperties":false
		}`,
	)

	return po.MustNewTypedTool(
		spec,
		nil,
		func(ctx context.Context, args CurrentDirectoryArgs, emit po.ToolUpdateEmitter) (po.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return po.ToolResult{}, err
			}

			path, err := getwd()
			if err != nil {
				return po.ToolResult{}, fmt.Errorf("get current directory: %w", err)
			}
			return po.NewTextToolResult(path, map[string]string{"path": path}, false)
		},
	)
}

// mustSpec 把静态 JSON Schema 转成经过协议校验的 ToolSpec。
func mustSpec(name, description, rawSchema string) po.ToolSpec {
	spec, err := po.NewToolSpec(name, description, json.RawMessage(rawSchema))
	if err != nil {
		panic(err)
	}
	return spec
}
