package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"

	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

type eventRecorder struct {
	mu      sync.Mutex
	opened  []string
	outputs []string
	exited  []string
	errors  []string
}

func (r *eventRecorder) TerminalOpened(ctx context.Context, sessionID string, cols int, rows int, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened = append(r.opened, sessionID)
	return nil
}

func (r *eventRecorder) TerminalOutput(ctx context.Context, sessionID string, direction string, payload string, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outputs = append(r.outputs, payload)
	return nil
}

func (r *eventRecorder) TerminalExited(ctx context.Context, sessionID string, exitCode int, reason string, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exited = append(r.exited, reason)
	return nil
}

func (r *eventRecorder) TerminalError(ctx context.Context, sessionID string, message string, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, message)
	return nil
}

func TestSelectShellFallback(t *testing.T) {
	t.Parallel()

	originalShell := os.Getenv("SHELL")
	t.Cleanup(func() {
		_ = os.Setenv("SHELL", originalShell)
	})
	_ = os.Setenv("SHELL", "/definitely/missing-shell")

	shell, err := selectShell()
	if err != nil {
		t.Fatalf("selectShell returned error: %v", err)
	}
	if !strings.HasPrefix(shell, "/bin/") {
		t.Fatalf("unexpected shell path: %q", shell)
	}
}

func TestNormalizeSizeDefaults(t *testing.T) {
	t.Parallel()

	cols, rows, err := normalizeSize(0, 0)
	if err != nil {
		t.Fatalf("normalizeSize returned error: %v", err)
	}
	if cols != defaultCols || rows != defaultRows {
		t.Fatalf("unexpected defaults: cols=%d rows=%d", cols, rows)
	}
}

func TestTerminalLifecycle(t *testing.T) {
	recorder := &eventRecorder{}
	manager := NewManager(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		recorder,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sessionID := "session-1"
	if err := manager.StartSession(ctx, sessionID, 80, 24, false); err != nil {
		if errors.Is(err, syscall.EPERM) || strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("PTY start is not permitted in this environment: %v", err)
		}
		t.Fatalf("StartSession returned error: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	command := base64.StdEncoding.EncodeToString([]byte("printf 'hello-from-pty\\n' && exit\n"))
	if err := manager.WriteInput(ctx, sessionID, command); err != nil {
		t.Fatalf("WriteInput returned error: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		recorder.mu.Lock()
		outputs := append([]string(nil), recorder.outputs...)
		exited := append([]string(nil), recorder.exited...)
		recorder.mu.Unlock()

		combinedOutput := ""
		for _, payload := range outputs {
			decoded, err := base64.StdEncoding.DecodeString(payload)
			if err != nil {
				t.Fatalf("failed to decode output payload: %v", err)
			}
			combinedOutput += string(decoded)
		}

		if strings.Contains(combinedOutput, "hello-from-pty") && len(exited) > 0 {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("terminal session did not emit expected output and exit events")
}

func TestStreamPTYOutputFlushesWithoutAdditionalInput(t *testing.T) {
	recorder := &eventRecorder{}
	manager := NewManager(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		recorder,
	)

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})

	session := &session{
		id:      "flush-session",
		ptyFile: reader,
	}

	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		manager.streamPTYOutput(context.Background(), session)
	}()

	if _, err := writer.Write([]byte("prompt> ")); err != nil {
		t.Fatalf("writer.Write returned error: %v", err)
	}

	deadline := time.Now().Add(outputFlushInterval * 10)
	for time.Now().Before(deadline) {
		recorder.mu.Lock()
		outputs := append([]string(nil), recorder.outputs...)
		recorder.mu.Unlock()

		if len(outputs) > 0 {
			decoded, decodeErr := base64.StdEncoding.DecodeString(outputs[0])
			if decodeErr != nil {
				t.Fatalf("failed to decode output payload: %v", decodeErr)
			}
			if string(decoded) != "prompt> " {
				t.Fatalf("unexpected flushed payload: %q", string(decoded))
			}

			_ = writer.Close()
			select {
			case <-streamDone:
			case <-time.After(2 * time.Second):
				t.Fatalf("streamPTYOutput did not exit after closing the pipe")
			}
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("terminal output was not flushed while the PTY read loop was idle")
}

func TestStartTerminalCommandFallsBackWithoutControllingTTY(t *testing.T) {
	originalStartWithSize := startPTYWithSize
	originalStartWithAttrs := startPTYWithAttrs
	t.Cleanup(func() {
		startPTYWithSize = originalStartWithSize
		startPTYWithAttrs = originalStartWithAttrs
	})

	startPTYWithSize = func(cmd *exec.Cmd, ws *pty.Winsize) (*os.File, error) {
		return nil, syscall.EPERM
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	startPTYWithAttrs = func(cmd *exec.Cmd, ws *pty.Winsize, attrs *syscall.SysProcAttr) (*os.File, error) {
		if attrs == nil || !attrs.Setpgid {
			t.Fatalf("expected process-group fallback attrs, got %+v", attrs)
		}

		return reader, nil
	}

	cmd, ptmx, mode, killProcessGroup, err := startTerminalCommand("/bin/sh", 80, 24, false)
	if err != nil {
		t.Fatalf("startTerminalCommand returned error: %v", err)
	}
	if cmd == nil || ptmx == nil {
		t.Fatalf("expected command and PTY file to be returned")
	}
	if mode != terminalStartModeNoControllingTTY {
		t.Fatalf("unexpected fallback mode: %q", mode)
	}
	if !killProcessGroup {
		t.Fatalf("expected process-group kill mode to remain enabled")
	}
}

func TestStartTerminalCommandFallsBackToMinimalMode(t *testing.T) {
	originalStartWithSize := startPTYWithSize
	originalStartWithAttrs := startPTYWithAttrs
	t.Cleanup(func() {
		startPTYWithSize = originalStartWithSize
		startPTYWithAttrs = originalStartWithAttrs
	})

	startPTYWithSize = func(cmd *exec.Cmd, ws *pty.Winsize) (*os.File, error) {
		return nil, syscall.EPERM
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	startPTYWithAttrs = func(cmd *exec.Cmd, ws *pty.Winsize, attrs *syscall.SysProcAttr) (*os.File, error) {
		if attrs != nil && attrs.Setpgid {
			return nil, syscall.EPERM
		}

		if attrs != nil {
			t.Fatalf("expected minimal fallback attrs to be nil, got %+v", attrs)
		}

		return reader, nil
	}

	cmd, ptmx, mode, killProcessGroup, err := startTerminalCommand("/bin/sh", 80, 24, false)
	if err != nil {
		t.Fatalf("startTerminalCommand returned error: %v", err)
	}
	if cmd == nil || ptmx == nil {
		t.Fatalf("expected command and PTY file to be returned")
	}
	if mode != terminalStartModeMinimal {
		t.Fatalf("unexpected fallback mode: %q", mode)
	}
	if killProcessGroup {
		t.Fatalf("expected minimal fallback to disable process-group kill mode")
	}
}
