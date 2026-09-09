// Package workspace 为编程工具提供限定在工作区内的文件系统访问能力。
package workspace

import (
	"fmt"
	"os"
)

// Workspace 表示允许 Coding Tool 访问的一棵目录树。
type Workspace struct {
	root *os.Root
}

// Open 打开一个workspace
func Open(rootPath string) (*Workspace, error) {
	if rootPath == "" {
		return nil, fmt.Errorf("workspace is required")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	return &Workspace{root: root}, nil
}

// Close 释放 Workspace 持有的 OS 资源。
func (w *Workspace) Close() error {
	return w.root.Close()
}

// Name 返回创建 Root 时使用的目录名。
func (w *Workspace) Name() string {
	return w.root.Name()
}

// ReadFile 读取 Workspace 内文件。
//
// name 应该是 Root 内部名称，例如：
//
//	"go.mod"
//	"src/main.go"
//
// 试图通过 .. 或越界 symlink 访问 Root 外位置时，os.Root 会返回错误。
func (w *Workspace) ReadFile(name string) ([]byte, error) {
	data, err := w.root.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read workspace file %q: %w", name, err)
	}
	return data, nil
}

// WriteFile 在 Workspace 内创建或覆盖文件。
//
// Workspace 只负责建立安全的文件边界。
func (w *Workspace) WriteFile(name string, data []byte, perm os.FileMode) error {
	if err := w.root.WriteFile(name, data, perm); err != nil {
		return fmt.Errorf("write workspace file %q: %w", name, err)
	}
	return nil
}

func (w *Workspace) Stat(name string) (os.FileInfo, error) {
	info, err := w.root.Stat(name)
	if err != nil {
		return nil, fmt.Errorf("stat workspace path %q: %w", name, err)
	}
	return info, nil
}

// ReadDir 枚举 Workspace 内的一层目录。
//
// Root.Open 会继续执行 os.Root 的路径边界检查；调用方不会拿到一个可以任意
// 跳出 Workspace 的宿主绝对路径。
func (w *Workspace) ReadDir(name string) ([]os.DirEntry, error) {
	file, err := w.root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open workspace directory %q: %w", name, err)
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read workspace directory %q: %w", name, err)
	}
	return entries, nil
}

// MkdirAll 在workspace内创建目录树
func (w *Workspace) MkdirAll(name string, perm os.FileMode) error {
	if err := w.root.MkdirAll(name, perm); err != nil {
		return fmt.Errorf("create workspace directory %q: %w", name, err)
	}
	return nil
}

// Rename 在 Workspace 内移动/替换路径。oldName/newName 都继续受 os.Root 的traversal-resistant 路径解析约束。
//
// Rename 是 edit/write 的提交点之一，但跨平台原子性和断电持久性属于更高层的
// 文件系统语义，不能只根据方法名作出保证。
func (w *Workspace) Rename(oldName, newName string) error {
	if err := w.root.Rename(oldName, newName); err != nil {
		return fmt.Errorf("rename workspace path %q -> %q: %w", oldName, newName, err)
	}
	return nil
}

// Remove 删除 Workspace 内的文件或空目录，主要用于清理临时文件。
func (w *Workspace) Remove(name string) error {
	if err := w.root.Remove(name); err != nil {
		return fmt.Errorf("remove workspace path %q: %w", name, err)
	}
	return nil
}
