
package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/creack/pty"
)

const (
	terminalStartModeControllingTTY   = "pty+ctty"
	terminalStartModeNoControllingTTY = "pty-no-ctty"
	terminalStartModeMinimal          = "pty-minimal"
)

var (
	startPTYWithSize  = pty.StartWithSize
	startPTYWithAttrs = pty.StartWithAttrs
)

func prepareTerminalCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func startTerminalCommand(shell string, cols int, rows int) (*exec.Cmd, *os.File, string, bool, error) {
	terminalSize := &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	}
	attempts := []struct {
		mode             string
		attrs            *syscall.SysProcAttr
		killProcessGroup bool
	}{
		{
			mode:             terminalStartModeControllingTTY,
			killProcessGroup: true,
		},
		{
			mode:             terminalStartModeNoControllingTTY,
			attrs:            &syscall.SysProcAttr{Setpgid: true},
			killProcessGroup: true,
		},
		{
			mode:             terminalStartModeMinimal,
			attrs:            nil,
			killProcessGroup: false,
		},
	}

	attemptErrors := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		cmd := newTerminalCommand(shell)

		var (
			ptmx *os.File
			err  error
		)
		if attempt.attrs == nil && attempt.mode == terminalStartModeControllingTTY {
			ptmx, err = startPTYWithSize(cmd, terminalSize)
		} else {
			ptmx, err = startPTYWithAttrs(cmd, terminalSize, attempt.attrs)
		}
		if err == nil {
			return cmd, ptmx, attempt.mode, attempt.killProcessGroup, nil
		}

		attemptErrors = append(attemptErrors, fmt.Sprintf("%s: %v", attempt.mode, err))
	}

	return nil, nil, "", false, fmt.Errorf("%s", strings.Join(attemptErrors, "; "))
}

func killTerminalCommand(cmd *exec.Cmd, killProcessGroup bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if !killProcessGroup {
		return cmd.Process.Kill()
	}

	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
