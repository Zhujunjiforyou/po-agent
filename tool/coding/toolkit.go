package coding

import (
	"encoding/json"
	"fmt"

	po "github.com/lemonzjj/po-agent-go"
)

// Toolkit 把操作同一个 Workspace capability 的只读 Coding Tools 组合在一起。
//
// 它故意不是 Plugin Framework。当前只需要一个很小的 Composition Object：
// read / grep / find / ls 共享同一文件系统边界，但 Agent Core 完全不知道这些工具
// 最终来自本地目录、容器、SSH 还是未来的远程文件系统。
type Toolkit struct {
	fs        FileSystem
	writable  WritableFileSystem
	mutations *mutationQueue
}

// NewToolkit 创建一组绑定到同一个 FileSystem capability 的 Coding Tools。
//
// 这里选择直接 panic nil，是因为 nil FileSystem 属于程序组装错误，而不是模型在
// 运行过程中可能恢复的业务错误；真正 Tool 参数错误仍然会正常返回 error。
func NewToolkit(files FileSystem) *Toolkit {
	if files == nil {
		panic("coding toolkit filesystem is required")
	}
	toolkit := &Toolkit{fs: files, mutations: newMutationQueue()}
	if writable, ok := files.(WritableFileSystem); ok {
		toolkit.writable = writable
	}
	return toolkit
}

// ReadOnlyTools 返回最小的代码探索工具集。
//
// 顺序本身不是协议要求，但保持稳定顺序有利于测试和 CLI 观察。
func (t *Toolkit) ReadOnlyTools() []po.Tool {
	return []po.Tool{t.newReadTool(), t.newGrepTool(), t.newFindTool(), t.newLSTool()}
}

// CodingTools 在只读工具上增加 edit / write。
//
// files 必须显式实现 WritableFileSystem；调用方不能仅仅因为“想注册写工具”，
// 就把一个原本只读的 capability 在运行时偷偷升级。
func (t *Toolkit) CodingTools() ([]po.Tool, error) {
	if t == nil || t.writable == nil {
		return nil, fmt.Errorf("coding toolkit filesystem is read-only")
	}
	tools := t.ReadOnlyTools()
	return append(tools, t.newEditTool(), t.newWriteTool()), nil
}

// mustSpec 把每个工具的 JSON Schema 构造集中起来。
// Schema 是开发期常量，如果这里失败说明代码本身写错，而不是模型输入错误。
func mustSpec(name, description, schema string) po.ToolSpec {
	spec, err := po.NewToolSpec(name, description, json.RawMessage(schema))
	if err != nil {
		panic(err)
	}
	return spec
}
