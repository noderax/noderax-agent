package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
	if runtime.GOOS == "windows" {
		t.Skip("PTY integration is unix-only in tests")
	}

	recorder := &eventRecorder{}
	manager := NewManager(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		recorder,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sessionID := "session-1"
	if err := manager.StartSession(ctx, sessionID, 80, 24); err != nil {
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
