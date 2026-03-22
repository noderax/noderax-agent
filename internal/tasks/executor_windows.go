//go:build windows

package tasks

import (
	"os/exec"
)

func prepareCommand(cmd *exec.Cmd) {
	// On Windows, the default Context cancellation sends an os.Kill to the main process,
	// which is typically sufficient unless Job Objects are used.
	// Implementing Job Objects requires cgo or x/sys/windows, which is out of scope.
}
