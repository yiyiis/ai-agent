//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// prepareCmd Unix 下放独立进程组，超时时可整组击杀（含 bash 派生的子进程）
func prepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree 击杀整个进程组
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
