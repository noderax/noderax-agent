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

const (
	TaskTypeShellExec      = "shell.exec"
	TaskTypePackageList    = "packageList"
	TaskTypePackageSearch  = "packageSearch"
	TaskTypePackageInstall = "packageInstall"
	TaskTypePackageRemove  = "packageRemove"
	TaskTypePackagePurge   = "packagePurge"
)

var (
	ErrUnsupportedTaskType             = errors.New("unsupported task type")
	ErrInvalidTaskPayload              = errors.New("invalid task payload")
	ErrUnsupportedExecutionEnvironment = errors.New("unsupported execution environment")
)

type ShellExecPayload struct {
	Command        string            `json:"command"`
	Shell          string            `json:"shell,omitempty"`
	WorkingDir     string            `json:"working_dir,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	Timeout        string            `json:"timeout,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type packageSearchPayload struct {
	Query string `json:"query"`
	Term  string `json:"term"`
}

type packageMutationPayload struct {
	Package  string   `json:"package,omitempty"`
	Packages []string `json:"packages,omitempty"`
	Names    []string `json:"names,omitempty"`
	Purge    bool     `json:"purge,omitempty"`
}

type ExecutionResult struct {
	ExitCode    int
	StartedAt   time.Time
	CompletedAt time.Time
	Duration    time.Duration
	Output      string
	Result      any
}

type commandSpec struct {
	name         string
	args         []string
	env          map[string]string
	dir          string
	startMessage string
	parseResult  func(string, func(string, string)) any
}

type commandRunner interface {
	SetEnv([]string)
	SetDir(string)
	StdoutPipe() (io.ReadCloser, error)
	StderrPipe() (io.ReadCloser, error)
	Start() error
	Wait() error
}

type execCommandRunner struct {
	cmd *exec.Cmd
}

func newExecCommandRunner(ctx context.Context, name string, args ...string) commandRunner {
	return &execCommandRunner{cmd: exec.CommandContext(ctx, name, args...)}
}

func (r *execCommandRunner) SetEnv(env []string) {
	r.cmd.Env = env
}

func (r *execCommandRunner) SetDir(dir string) {
	r.cmd.Dir = dir
}

func (r *execCommandRunner) StdoutPipe() (io.ReadCloser, error) {
	return r.cmd.StdoutPipe()
}

func (r *execCommandRunner) StderrPipe() (io.ReadCloser, error) {
	return r.cmd.StderrPipe()
}

func (r *execCommandRunner) Start() error {
	return r.cmd.Start()
}

func (r *execCommandRunner) Wait() error {
	return r.cmd.Wait()
}

type ShellExecutor struct {
	defaultTimeout time.Duration
	goos           string
	lookPath       func(string) (string, error)
	newCommand     func(context.Context, string, ...string) commandRunner
}

func NewShellExecutor(defaultTimeout time.Duration) *ShellExecutor {
	return &ShellExecutor{
		defaultTimeout: defaultTimeout,
		goos:           runtime.GOOS,
		lookPath:       exec.LookPath,
		newCommand:     newExecCommandRunner,
	}
}

func (e *ShellExecutor) TimeoutFor(task api.Task) time.Duration {
	if task.TimeoutSeconds > 0 {
		return time.Duration(task.TimeoutSeconds) * time.Second
	}

	if task.Type != TaskTypeShellExec {
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
	spec, err := e.commandForTask(task)
	if err != nil {
		emitLog(onLog, "system", err.Error())
		return ExecutionResult{ExitCode: -1}, err
	}

	cmd := e.newCommand(ctx, spec.name, spec.args...)
	cmd.SetEnv(mergeEnvironment(spec.env))
	if spec.dir != "" {
		cmd.SetDir(spec.dir)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("prepare stdout pipe: %w", err)
		emitLog(onLog, "system", err.Error())
		return ExecutionResult{ExitCode: -1}, err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("prepare stderr pipe: %w", err)
		emitLog(onLog, "system", err.Error())
		return ExecutionResult{ExitCode: -1}, err
	}

	emitLog(onLog, "system", spec.startMessage)

	startedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		err = fmt.Errorf("start command: %w", err)
		emitLog(onLog, "system", err.Error())
		return ExecutionResult{ExitCode: -1}, err
	}

	var scanWG sync.WaitGroup
	scanWG.Add(2)

	var mx sync.Mutex
	var outBuilder strings.Builder
	captureLog := func(stream, line string) {
		if stream == "stdout" {
			mx.Lock()
			if outBuilder.Len() < 1024*1024*2 { // 2MB limit
				outBuilder.WriteString(line)
				outBuilder.WriteByte('\n')
			}
			mx.Unlock()
		}
		if onLog != nil {
			onLog(stream, line)
		}
	}

	go scanOutput(&scanWG, stdout, "stdout", captureLog)
	go scanOutput(&scanWG, stderr, "stderr", captureLog)

	waitErr := cmd.Wait()
	scanWG.Wait()

	completedAt := time.Now().UTC()
	rawOutput := outBuilder.String()

	var parsedResult any
	if spec.parseResult != nil {
		parsedResult = spec.parseResult(rawOutput, onLog)
	}

	result := ExecutionResult{
		ExitCode:    exitCode(waitErr),
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    completedAt.Sub(startedAt),
		Output:      rawOutput,
		Result:      parsedResult,
	}

	emitLog(onLog, "system", fmt.Sprintf("command finished with exit code %d", result.ExitCode))

	if waitErr != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, waitErr
	}

	return result, nil
}

func (e *ShellExecutor) commandForTask(task api.Task) (commandSpec, error) {
	switch task.Type {
	case TaskTypeShellExec:
		return e.shellCommand(task.Payload)
	case TaskTypePackageList:
		return e.packageListCommand()
	case TaskTypePackageSearch:
		return e.packageSearchCommand(task.Payload)
	case TaskTypePackageInstall:
		return e.packageInstallCommand(task.Payload)
	case TaskTypePackageRemove:
		return e.packageRemoveCommand(task.Payload)
	case TaskTypePackagePurge:
		return e.packagePurgeCommand(task.Payload)
	default:
		return commandSpec{}, fmt.Errorf("%w: %s", ErrUnsupportedTaskType, task.Type)
	}
}

func (e *ShellExecutor) shellCommand(payload json.RawMessage) (commandSpec, error) {
	parsed, err := decodeShellPayload(payload)
	if err != nil {
		return commandSpec{}, err
	}
	if strings.TrimSpace(parsed.Command) == "" {
		return commandSpec{}, fmt.Errorf("%w: shell.exec payload requires a command", ErrInvalidTaskPayload)
	}

	commandName, args := buildShellCommand(e.goos, parsed)
	return commandSpec{
		name:         commandName,
		args:         args,
		env:          parsed.Environment,
		dir:          parsed.WorkingDir,
		startMessage: fmt.Sprintf("running shell.exec command via %s", commandName),
	}, nil
}

func (e *ShellExecutor) packageListCommand() (commandSpec, error) {
	if err := e.ensureLinuxTaskSupport(); err != nil {
		return commandSpec{}, err
	}

	if dpkgPath, err := e.lookPath("dpkg"); err == nil {
		args := []string{"-l"}
		return commandSpec{
			name:         dpkgPath,
			args:         args,
			startMessage: fmt.Sprintf("running %s", formatCommandForLog(dpkgPath, args)),
			parseResult:  parsePackageList,
		}, nil
	}

	aptPath, err := e.requireBinary("apt")
	if err != nil {
		return commandSpec{}, err
	}

	args := []string{"list", "--installed"}
	return commandSpec{
		name:         aptPath,
		args:         args,
		startMessage: fmt.Sprintf("running %s", formatCommandForLog(aptPath, args)),
		parseResult:  parsePackageList,
	}, nil
}

func (e *ShellExecutor) packageSearchCommand(payload json.RawMessage) (commandSpec, error) {
	if err := e.ensureLinuxTaskSupport(); err != nil {
		return commandSpec{}, err
	}

	var parsed packageSearchPayload
	if err := decodePayload(payload, &parsed, TaskTypePackageSearch); err != nil {
		return commandSpec{}, err
	}

	query := strings.TrimSpace(parsed.Query)
	if query == "" {
		query = strings.TrimSpace(parsed.Term)
	}

	terms := strings.Fields(query)
	if len(terms) == 0 {
		return commandSpec{}, fmt.Errorf("%w: packageSearch payload requires a non-empty query", ErrInvalidTaskPayload)
	}
	for _, term := range terms {
		if strings.HasPrefix(term, "-") {
			return commandSpec{}, fmt.Errorf("%w: packageSearch query term %q must not start with '-'", ErrInvalidTaskPayload, term)
		}
	}

	aptPath, err := e.requireBinary("apt")
	if err != nil {
		return commandSpec{}, err
	}

	args := append([]string{"search"}, terms...)
	return commandSpec{
		name:         aptPath,
		args:         args,
		startMessage: fmt.Sprintf("running %s", formatCommandForLog(aptPath, args)),
		parseResult:  parsePackageSearch,
	}, nil
}

func (e *ShellExecutor) packageInstallCommand(payload json.RawMessage) (commandSpec, error) {
	if err := e.ensureLinuxTaskSupport(); err != nil {
		return commandSpec{}, err
	}

	aptGetPath, err := e.requireBinary("apt-get")
	if err != nil {
		return commandSpec{}, err
	}

	packages, err := decodePackageNames(payload)
	if err != nil {
		return commandSpec{}, err
	}

	args := append([]string{"install", "-y", "--"}, packages...)
	return commandSpec{
		name:         aptGetPath,
		args:         args,
		env:          map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		startMessage: fmt.Sprintf("running %s", formatCommandForLog(aptGetPath, args)),
	}, nil
}

func (e *ShellExecutor) packageRemoveCommand(payload json.RawMessage) (commandSpec, error) {
	if err := e.ensureLinuxTaskSupport(); err != nil {
		return commandSpec{}, err
	}

	aptGetPath, err := e.requireBinary("apt-get")
	if err != nil {
		return commandSpec{}, err
	}

	var parsed packageMutationPayload
	if err := decodePayload(payload, &parsed, TaskTypePackageRemove); err != nil {
		return commandSpec{}, err
	}

	packages, err := normalizePackageNames(parsed)
	if err != nil {
		return commandSpec{}, err
	}

	action := "remove"
	if parsed.Purge {
		action = "purge"
	}

	args := append([]string{action, "-y", "--"}, packages...)
	return commandSpec{
		name:         aptGetPath,
		args:         args,
		env:          map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		startMessage: fmt.Sprintf("running %s", formatCommandForLog(aptGetPath, args)),
	}, nil
}

func (e *ShellExecutor) packagePurgeCommand(payload json.RawMessage) (commandSpec, error) {
	if err := e.ensureLinuxTaskSupport(); err != nil {
		return commandSpec{}, err
	}

	aptGetPath, err := e.requireBinary("apt-get")
	if err != nil {
		return commandSpec{}, err
	}

	var parsed packageMutationPayload
	if err := decodePayload(payload, &parsed, TaskTypePackagePurge); err != nil {
		return commandSpec{}, err
	}

	packages, err := normalizePackageNames(parsed)
	if err != nil {
		return commandSpec{}, err
	}

	args := append([]string{"purge", "-y", "--"}, packages...)
	return commandSpec{
		name:         aptGetPath,
		args:         args,
		env:          map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		startMessage: fmt.Sprintf("running %s", formatCommandForLog(aptGetPath, args)),
	}, nil
}

func (e *ShellExecutor) ensureLinuxTaskSupport() error {
	if e.goos != "linux" {
		return fmt.Errorf("%w: package tasks require linux, got %s", ErrUnsupportedExecutionEnvironment, e.goos)
	}

	return nil
}

func (e *ShellExecutor) requireBinary(name string) (string, error) {
	path, err := e.lookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: required binary %q is not available", ErrUnsupportedExecutionEnvironment, name)
	}
	return path, nil
}

func decodeShellPayload(payload json.RawMessage) (ShellExecPayload, error) {
	var parsed ShellExecPayload
	if len(payload) == 0 {
		return ShellExecPayload{}, fmt.Errorf("%w: missing shell.exec payload", ErrInvalidTaskPayload)
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return ShellExecPayload{}, fmt.Errorf("%w: decode shell.exec payload: %v", ErrInvalidTaskPayload, err)
	}
	return parsed, nil
}

func decodePayload(payload json.RawMessage, target any, taskType string) error {
	if len(payload) == 0 {
		return fmt.Errorf("%w: missing %s payload", ErrInvalidTaskPayload, taskType)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("%w: decode %s payload: %v", ErrInvalidTaskPayload, taskType, err)
	}
	return nil
}

func decodePackageNames(payload json.RawMessage) ([]string, error) {
	var parsed packageMutationPayload
	if err := decodePayload(payload, &parsed, TaskTypePackageInstall); err != nil {
		return nil, err
	}

	return normalizePackageNames(parsed)
}

func normalizePackageNames(payload packageMutationPayload) ([]string, error) {
	rawNames := make([]string, 0, len(payload.Packages)+len(payload.Names)+1)
	if payload.Package != "" {
		rawNames = append(rawNames, payload.Package)
	}
	rawNames = append(rawNames, payload.Packages...)
	rawNames = append(rawNames, payload.Names...)

	packages := make([]string, 0, len(rawNames))
	for _, raw := range rawNames {
		name := strings.TrimSpace(raw)
		switch {
		case name == "":
			return nil, fmt.Errorf("%w: package names must not be empty", ErrInvalidTaskPayload)
		case strings.HasPrefix(name, "-"):
			return nil, fmt.Errorf("%w: invalid package name %q", ErrInvalidTaskPayload, name)
		default:
			packages = append(packages, name)
		}
	}

	if len(packages) == 0 {
		return nil, fmt.Errorf("%w: package list must not be empty", ErrInvalidTaskPayload)
	}

	return packages, nil
}

func buildShellCommand(goos string, payload ShellExecPayload) (string, []string) {
	if goos == "windows" {
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

func formatCommandForLog(name string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, name)
	for _, arg := range args {
		if strings.ContainsAny(arg, " \t\r\n\"'") {
			parts = append(parts, strconvQuote(arg))
			continue
		}
		parts = append(parts, arg)
	}

	return strings.Join(parts, " ")
}

func strconvQuote(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	return "\"" + escaped + "\""
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
		emitLog(onLog, stream, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		emitLog(onLog, "system", fmt.Sprintf("log stream error: %v", err))
	}
}

func emitLog(onLog func(string, string), stream, line string) {
	if onLog == nil {
		return
	}
	onLog(stream, line)
}

type PackageInfo struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	Description  string `json:"description,omitempty"`
}

func parsePackageList(output string, onLog func(string, string)) any {
	results := make([]PackageInfo, 0)
	lines := strings.Split(output, "\n")
	
	var formatDpkg, formatApt, formatCompact int

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// dpkg -l format
		if strings.HasPrefix(line, "ii ") || strings.HasPrefix(line, "hi ") || strings.HasPrefix(line, "rc ") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				results = append(results, PackageInfo{
					Name:         fields[1],
					Version:      fields[2],
					Architecture: fields[3],
					Description:  strings.Join(fields[4:], " "),
				})
				formatDpkg++
			}
			continue
		}

		// apt list --installed format
		if strings.Contains(line, "[installed") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				namePart := strings.SplitN(fields[0], "/", 2)[0]
				results = append(results, PackageInfo{
					Name:         namePart,
					Version:      fields[1],
					Architecture: fields[2],
				})
				formatApt++
			}
			continue
		}

		// compact name:version format
		if strings.Contains(line, ":") && len(strings.Fields(line)) == 1 {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
				results = append(results, PackageInfo{
					Name:    parts[0],
					Version: parts[1],
				})
				formatCompact++
			}
			continue
		}
	}

	hasOutput := len(strings.TrimSpace(output)) > 0
	if !hasOutput {
		if onLog != nil {
			emitLog(onLog, "system", "error: command output is completely empty")
		}
	} else if len(results) == 0 {
		if onLog != nil {
			emitLog(onLog, "system", "error: parse failure, unable to extract any packages from output (parse count is zero)")
		}
		return nil
	}

	if onLog != nil {
		formatDetected := "unknown"
		if formatDpkg > 0 {
			formatDetected = "dpkg"
		} else if formatApt > 0 {
			formatDetected = "apt"
		} else if formatCompact > 0 {
			formatDetected = "compact"
		}
		emitLog(onLog, "system", fmt.Sprintf("parsed %d packages (format: %s)", len(results), formatDetected))
	}

	return map[string]any{
		"packages": results,
	}
}

func parsePackageSearch(output string, onLog func(string, string)) any {
	results := make([]PackageInfo, 0)
	lines := strings.Split(output, "\n")
	
	var currentPkg *PackageInfo
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r\t ")
		if line == "" || strings.HasPrefix(line, "Sorting...") || strings.HasPrefix(line, "Full Text Search...") {
			continue
		}

		if !strings.HasPrefix(line, " ") {
			// pkg line
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if currentPkg != nil {
					results = append(results, *currentPkg)
				}
				namePart := strings.SplitN(fields[0], "/", 2)[0]
				currentPkg = &PackageInfo{
					Name:    namePart,
					Version: fields[1],
				}
				if len(fields) >= 3 {
					currentPkg.Architecture = fields[2]
				}
			}
		} else if currentPkg != nil {
			// desc line
			desc := strings.TrimSpace(line)
			if currentPkg.Description == "" {
				currentPkg.Description = desc
			} else {
				currentPkg.Description += " " + desc
			}
		}
	}

	if currentPkg != nil {
		results = append(results, *currentPkg)
	}

	hasOutput := len(strings.TrimSpace(output)) > 0
	if hasOutput && len(results) == 0 {
		if onLog != nil {
			emitLog(onLog, "system", "parse failure: unable to extract any package search results from output")
		}
		return nil
	}

	if onLog != nil {
		emitLog(onLog, "system", fmt.Sprintf("parsed %d search results", len(results)))
	}

	return map[string]any{
		"results": results,
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
