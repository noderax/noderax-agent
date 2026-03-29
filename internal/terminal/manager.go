package terminal

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

const (
	defaultCols          = 120
	defaultRows          = 34
	outputFlushInterval  = 25 * time.Millisecond
	outputFlushThreshold = 1024
)

var (
	ErrSessionExists       = errors.New("terminal session already exists")
	ErrSessionNotFound     = errors.New("terminal session was not found")
	ErrUnsupportedPlatform = errors.New("interactive terminal is only supported on unix-like platforms")
	ErrInvalidTerminalSize = errors.New("terminal size is invalid")
)

type RealtimeEvents interface {
	TerminalOpened(context.Context, string, int, int, time.Time) error
	TerminalOutput(context.Context, string, string, string, time.Time) error
	TerminalExited(context.Context, string, int, string, time.Time) error
	TerminalError(context.Context, string, string, time.Time) error
}

type session struct {
	id               string
	cmd              *exec.Cmd
	ptyFile          *os.File
	killProcessGroup bool
	cols             int
	rows             int
	done             chan struct{}
	stopReason       string
	stopReasonLock   sync.Mutex
}

type Manager struct {
	logger *slog.Logger
	events RealtimeEvents

	mu       sync.Mutex
	sessions map[string]*session
}

func NewManager(logger *slog.Logger, events RealtimeEvents) *Manager {
	return &Manager{
		logger:   logger,
		events:   events,
		sessions: make(map[string]*session),
	}
}

func (m *Manager) StartSession(
	ctx context.Context,
	sessionID string,
	cols int,
	rows int,
) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session ID is required")
	}

	normalizedCols, normalizedRows, err := normalizeSize(cols, rows)
	if err != nil {
		return err
	}

	shellCandidates := selectShellCandidates()
	if len(shellCandidates) == 0 {
		return fmt.Errorf("unable to resolve an interactive shell")
	}

	var (
		cmd              *exec.Cmd
		ptmx             *os.File
		startMode        string
		killProcessGroup bool
		startErrors      []string
	)

	for _, shell := range shellCandidates {
		candidateCmd, candidatePTY, candidateMode, candidateKillProcessGroup, candidateErr := startTerminalCommand(
			shell,
			normalizedCols,
			normalizedRows,
		)
		if candidateErr == nil {
			cmd = candidateCmd
			ptmx = candidatePTY
			startMode = candidateMode
			killProcessGroup = candidateKillProcessGroup
			break
		}

		startErrors = append(startErrors, fmt.Sprintf("%s: %v", shell, candidateErr))
		m.logger.Warn(
			"failed to start terminal shell candidate",
			"session_id", sessionID,
			"shell", shell,
			"error", candidateErr,
		)
	}

	if ptmx == nil || cmd == nil {
		return fmt.Errorf(
			"start PTY shell: %s",
			strings.Join(startErrors, "; "),
		)
	}

	s := &session{
		id:               sessionID,
		cmd:              cmd,
		ptyFile:          ptmx,
		killProcessGroup: killProcessGroup,
		cols:             normalizedCols,
		rows:             normalizedRows,
		done:             make(chan struct{}),
	}

	m.mu.Lock()
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return ErrSessionExists
	}
	m.sessions[sessionID] = s
	m.mu.Unlock()

	if startMode != "" && startMode != terminalStartModeControllingTTY {
		m.logger.Info(
			"started terminal session using PTY fallback mode",
			"session_id", sessionID,
			"start_mode", startMode,
		)
	}

	go m.runSession(ctx, s)

	if m.events != nil {
		if err := m.events.TerminalOpened(context.Background(), sessionID, normalizedCols, normalizedRows, time.Now().UTC()); err != nil {
			m.logger.Warn("failed to emit terminal opened", "session_id", sessionID, "error", err)
		}
	}

	return nil
}

func (m *Manager) WriteInput(
	ctx context.Context,
	sessionID string,
	payload string,
) error {
	s, err := m.getSession(sessionID)
	if err != nil {
		return err
	}

	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return fmt.Errorf("decode terminal input payload: %w", err)
	}

	if len(data) == 0 {
		return nil
	}

	_, err = s.ptyFile.Write(data)
	if err != nil {
		return fmt.Errorf("write terminal input: %w", err)
	}

	return nil
}

func (m *Manager) ResizeSession(
	ctx context.Context,
	sessionID string,
	cols int,
	rows int,
) error {
	s, err := m.getSession(sessionID)
	if err != nil {
		return err
	}

	normalizedCols, normalizedRows, err := normalizeSize(cols, rows)
	if err != nil {
		return err
	}

	if err := pty.Setsize(s.ptyFile, &pty.Winsize{
		Cols: uint16(normalizedCols),
		Rows: uint16(normalizedRows),
	}); err != nil {
		return fmt.Errorf("resize PTY: %w", err)
	}

	s.cols = normalizedCols
	s.rows = normalizedRows
	return nil
}

func (m *Manager) StopSession(
	ctx context.Context,
	sessionID string,
	reason string,
) error {
	s, err := m.getSession(sessionID)
	if err != nil {
		return err
	}

	s.stopReasonLock.Lock()
	s.stopReason = strings.TrimSpace(reason)
	s.stopReasonLock.Unlock()

	if err := killTerminalCommand(s.cmd, s.killProcessGroup); err != nil {
		return fmt.Errorf("stop terminal session: %w", err)
	}

	return nil
}

func (m *Manager) runSession(ctx context.Context, s *session) {
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = m.StopSession(context.Background(), s.id, "terminal session context canceled")
			case <-s.done:
			}
		}()
	}
	defer close(s.done)

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		m.streamPTYOutput(ctx, s)
	}()

	waitErr := s.cmd.Wait()
	_ = s.ptyFile.Close()
	<-readerDone

	exitCode := exitCode(waitErr)
	reason := strings.TrimSpace(readStopReason(s))
	if reason == "" {
		if waitErr != nil {
			reason = waitErr.Error()
		} else {
			reason = "terminal session exited"
		}
	}

	m.mu.Lock()
	delete(m.sessions, s.id)
	m.mu.Unlock()

	if m.events != nil {
		if err := m.events.TerminalExited(context.Background(), s.id, exitCode, reason, time.Now().UTC()); err != nil {
			m.logger.Warn("failed to emit terminal exit", "session_id", s.id, "error", err)
		}
	}
}

func (m *Manager) streamPTYOutput(ctx context.Context, s *session) {
	type readResult struct {
		data []byte
		err  error
	}

	buffer := make([]byte, 4096)
	var pending bytes.Buffer
	ticker := time.NewTicker(outputFlushInterval)
	defer ticker.Stop()
	readResults := make(chan readResult, 1)

	go func() {
		for {
			n, err := s.ptyFile.Read(buffer)
			payload := append([]byte(nil), buffer[:n]...)

			readResults <- readResult{
				data: payload,
				err:  err,
			}

			if err != nil {
				return
			}
		}
	}()

	flush := func() {
		if pending.Len() == 0 {
			return
		}

		payload := make([]byte, pending.Len())
		copy(payload, pending.Bytes())
		pending.Reset()

		if m.events != nil {
			encoded := base64.StdEncoding.EncodeToString(payload)
			if err := m.events.TerminalOutput(context.Background(), s.id, "stdout", encoded, time.Now().UTC()); err != nil {
				m.logger.Warn("failed to emit terminal output", "session_id", s.id, "error", err)
			}
		}
	}

	for {
		select {
		case <-ticker.C:
			flush()
		case result := <-readResults:
			if len(result.data) > 0 {
				pending.Write(result.data)
				if pending.Len() >= outputFlushThreshold {
					flush()
				}
			}

			if result.err != nil {
				flush()
				if !errors.Is(result.err, os.ErrClosed) && !strings.Contains(strings.ToLower(result.err.Error()), "input/output error") && !errors.Is(result.err, context.Canceled) {
					if m.events != nil {
						_ = m.events.TerminalError(context.Background(), s.id, result.err.Error(), time.Now().UTC())
					}
				}
				return
			}
		}
	}
}

func (m *Manager) getSession(sessionID string) (*session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	return s, nil
}

func normalizeSize(cols int, rows int) (int, int, error) {
	if cols == 0 {
		cols = defaultCols
	}
	if rows == 0 {
		rows = defaultRows
	}
	if cols < 20 || rows < 10 {
		return 0, 0, ErrInvalidTerminalSize
	}

	return cols, rows, nil
}

func selectShell() (string, error) {
	candidates := selectShellCandidates()
	if len(candidates) == 0 {
		return "", fmt.Errorf("unable to resolve an interactive shell")
	}

	return candidates[0], nil
}

func selectShellCandidates() []string {
	candidates := []string{
		strings.TrimSpace(os.Getenv("SHELL")),
		"/bin/bash",
		"/bin/zsh",
		"/bin/sh",
	}

	resolvedCandidates := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}

		if !strings.HasPrefix(candidate, "/") {
			if resolvedPath, err := exec.LookPath(candidate); err == nil {
				if _, ok := seen[resolvedPath]; !ok {
					seen[resolvedPath] = struct{}{}
					resolvedCandidates = append(resolvedCandidates, resolvedPath)
				}
			}
			continue
		}

		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if _, ok := seen[candidate]; !ok {
				seen[candidate] = struct{}{}
				resolvedCandidates = append(resolvedCandidates, candidate)
			}
		}
	}

	return resolvedCandidates
}

func newTerminalCommand(shell string) *exec.Cmd {
	cmd := exec.Command(shell, "-i")
	prepareTerminalCommand(cmd)
	cmd.Env = mergeEnvironment(map[string]string{
		"TERM":  "xterm-256color",
		"PAGER": "cat",
	})
	cmd.Dir = resolveWorkingDir()
	return cmd
}

func resolveWorkingDir() string {
	if homeDir, err := os.UserHomeDir(); err == nil && strings.TrimSpace(homeDir) != "" {
		return homeDir
	}

	if currentDir, err := os.Getwd(); err == nil && strings.TrimSpace(currentDir) != "" {
		return filepath.Clean(currentDir)
	}

	return "/"
}

func mergeEnvironment(overrides map[string]string) []string {
	envMap := map[string]string{}
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	for key, value := range overrides {
		envMap[key] = value
	}

	env := make([]string, 0, len(envMap))
	for key, value := range envMap {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	return env
}

func readStopReason(s *session) string {
	s.stopReasonLock.Lock()
	defer s.stopReasonLock.Unlock()
	return s.stopReason
}

func exitCode(waitErr error) int {
	if waitErr == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}

	return -1
}
