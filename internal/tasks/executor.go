package tasks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

var ErrUnsupportedTaskType = errors.New("unsupported task type")

type ShellExecPayload struct {
	Command        string            `json:"command"`
	Shell          string            `json:"shell,omitempty"`
	WorkingDir     string            `json:"working_dir,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	Timeout        string            `json:"timeout,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type ExecutionResult struct {
	ExitCode    int
	StartedAt   time.Time
	CompletedAt time.Time
	Duration    time.Duration
}

type ShellExecutor struct {
	defaultTimeout time.Duration
}

func NewShellExecutor(defaultTimeout time.Duration) *ShellExecutor {
	return &ShellExecutor{defaultTimeout: defaultTimeout}
}

func (e *ShellExecutor) TimeoutFor(task api.Task) time.Duration {
	if task.TimeoutSeconds > 0 {
		return time.Duration(task.TimeoutSeconds) * time.Second
	}

	if task.Type != "shell.exec" {
		return e.defaultTimeout
	}

	payload, err := decodeShellPayload(task.Payload)
	if err != nil {
		return e.defaultTimeout
	}
	if payload.TimeoutSeconds > 0 {
		return time.Duration(payload.TimeoutSeconds) * time.Second
	}
	if payload.Timeout != "" {
		duration, err := time.ParseDuration(payload.Timeout)
		if err == nil && duration > 0 {
			return duration
		}
	}

	return e.defaultTimeout
}

func (e *ShellExecutor) Execute(ctx context.Context, task api.Task, onLog func(string, string)) (ExecutionResult, error) {
	if task.Type != "shell.exec" {
		return ExecutionResult{ExitCode: -1}, fmt.Errorf("%w: %s", ErrUnsupportedTaskType, task.Type)
	}

	payload, err := decodeShellPayload(task.Payload)
	if err != nil {
		return ExecutionResult{ExitCode: -1}, err
	}
	if strings.TrimSpace(payload.Command) == "" {
		return ExecutionResult{ExitCode: -1}, fmt.Errorf("shell.exec payload requires a command")
	}

	commandName, args := buildCommand(payload)
	cmd := exec.CommandContext(ctx, commandName, args...)
	cmd.Env = mergeEnvironment(payload.Environment)
	if payload.WorkingDir != "" {
		cmd.Dir = payload.WorkingDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ExecutionResult{ExitCode: -1}, fmt.Errorf("prepare stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return ExecutionResult{ExitCode: -1}, fmt.Errorf("prepare stderr pipe: %w", err)
	}

	startedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		return ExecutionResult{ExitCode: -1}, fmt.Errorf("start command: %w", err)
	}

	var scanWG sync.WaitGroup
	scanWG.Add(2)
	go scanOutput(&scanWG, stdout, "stdout", onLog)
	go scanOutput(&scanWG, stderr, "stderr", onLog)

	waitErr := cmd.Wait()
	scanWG.Wait()

	completedAt := time.Now().UTC()
	result := ExecutionResult{
		ExitCode:    exitCode(waitErr),
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    completedAt.Sub(startedAt),
	}

	if waitErr != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, waitErr
	}

	return result, nil
}

func decodeShellPayload(payload json.RawMessage) (ShellExecPayload, error) {
	var parsed ShellExecPayload
	if len(payload) == 0 {
		return ShellExecPayload{}, fmt.Errorf("missing task payload")
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return ShellExecPayload{}, fmt.Errorf("decode shell.exec payload: %w", err)
	}
	return parsed, nil
}

func buildCommand(payload ShellExecPayload) (string, []string) {
	if runtime.GOOS == "windows" {
		shell := payload.Shell
		if shell == "" {
			shell = "cmd.exe"
		}

		base := strings.ToLower(filepath.Base(shell))
		if strings.Contains(base, "powershell") {
			return shell, []string{"-Command", payload.Command}
		}

		return shell, []string{"/C", payload.Command}
	}

	shell := payload.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell, []string{"-lc", payload.Command}
}

func mergeEnvironment(overrides map[string]string) []string {
	envMap := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		envMap[key] = value
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

func scanOutput(wg *sync.WaitGroup, reader io.ReadCloser, stream string, onLog func(string, string)) {
	defer wg.Done()
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)

	for scanner.Scan() {
		onLog(stream, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		onLog("system", fmt.Sprintf("log stream error: %v", err))
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	return -1
}
