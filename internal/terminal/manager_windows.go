//go:build windows

package terminal

import "os/exec"

func prepareTerminalCommand(cmd *exec.Cmd) {}

func killTerminalCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	return cmd.Process.Kill()
}
