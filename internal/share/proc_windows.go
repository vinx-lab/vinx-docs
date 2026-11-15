//go:build windows

package share

import "os/exec"

// Windows 不在支持范围内（spec 0008），这里只保证能编译。
func setGroup(cmd *exec.Cmd) {}

func terminate(cmd *exec.Cmd) { killGroup(cmd) }

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
