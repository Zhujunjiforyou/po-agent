//go:build windows

package process

import "os/exec"

func configureProcessGroup(cmd *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// Windows 的完整 descendant-tree termination 通常需要 Job Object。
	// 当前 baseline 至少终止直接 child；文档明确这一平台限制。
	return cmd.Process.Kill()
}
