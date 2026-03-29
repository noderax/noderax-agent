//go:build windows

package terminal

import (
	"fmt"
	"os"
	"os/exec"
)

const terminalStartModeControllingTTY = "pty+ctty"

func prepareTerminalCommand(cmd *exec.Cmd) {}

func startTerminalCommand(shell string, cols int, rows int) (*exec.Cmd, *os.File, string, bool, error) {
	return nil, nil, "", false, fmt.Errorf("%w", ErrUnsupportedPlatform)
}

func killTerminalCommand(cmd *exec.Cmd, killProcessGroup bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	return cmd.Process.Kill()
}
