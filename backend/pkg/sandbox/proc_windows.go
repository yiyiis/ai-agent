//go:build windows

package sandbox

import (
	"os/exec"
	"strconv"
)

// prepareCmd Windows 无进程组语义，树击杀交给 killTree 的 taskkill
func prepareCmd(cmd *exec.Cmd) {}

// killTree Windows 下用 taskkill /T 击杀整棵进程树
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
}
