package coding

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	po "github.com/lemonzjj/po-agent-go"
)

const workspaceResourceKey = "workspace"

// ResolveResources 根据 Po 内置 Workspace Tool 的名称和参数推导调度资源。
// Raw Shell 和 Go 命令无法可靠分析实际访问范围，因此保守地独占整个 Workspace。
func ResolveResources(call po.ToolCall) ([]po.ResourceClaim, error) {
	switch call.Name {
	case "read":
		return fileResourceClaims(call, po.ResourceAccessShared)
	case "edit", "write":
		return fileResourceClaims(call, po.ResourceAccessExclusive)
	case "grep", "find", "ls", "git":
		return workspaceClaims(po.ResourceAccessShared), nil
	case "go", "shell":
		return workspaceClaims(po.ResourceAccessExclusive), nil
	default:
		// 未分类的 Workspace Tool 无法证明自己没有副作用，默认作为全局屏障。
		return workspaceClaims(po.ResourceAccessExclusive), nil
	}
}

func fileResourceClaims(call po.ToolCall, mode po.ResourceAccessMode) ([]po.ResourceClaim, error) {
	path, err := resourcePath(call)
	if err != nil {
		return nil, err
	}
	return []po.ResourceClaim{
		{Key: workspaceResourceKey, Mode: po.ResourceAccessShared},
		{Key: "file:" + filepath.ToSlash(path), Mode: mode},
	}, nil
}

func workspaceClaims(mode po.ResourceAccessMode) []po.ResourceClaim {
	return []po.ResourceClaim{{Key: workspaceResourceKey, Mode: mode}}
}

func resourcePath(call po.ToolCall) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("%w: decode resource path: %v", po.ErrInvalidToolArguments, err)
	}
	path, err := normalizeWorkspacePath(args.Path, false)
	if err != nil {
		return "", fmt.Errorf("%w: %v", po.ErrInvalidToolArguments, err)
	}
	return path, nil
}
