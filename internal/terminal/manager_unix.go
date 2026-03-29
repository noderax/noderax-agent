//go:build !windows

package terminal

import (
	"os/exec"
	"syscall"
)

func prepareTerminalCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killTerminalCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
