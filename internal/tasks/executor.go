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
	TaskTypeAgentUpdate    = "agent.update"
	TaskTypeLogScan        = "log.scan"
	TaskTypeShellExec      = "shell.exec"
	TaskTypePackageList    = "packageList"
	TaskTypePackageSearch  = "packageSearch"
	TaskTypePackageInstall = "packageInstall"
	TaskTypePackageRemove  = "packageRemove"
	TaskTypePackagePurge   = "packagePurge"

	linuxPrivilegedUpdateHelperPath  = "/usr/local/libexec/noderax-agent-self-update"
	linuxPrivilegedUpdateRequestPath = "/var/lib/noderax-agent/update-request.json"
	linuxPackageMutationHelperPath   = "/usr/local/libexec/noderax-agent-package-mutation"
	linuxPackageMutationRequestPath  = "/var/lib/noderax-agent/package-mutation-request.txt"
	linuxOperationalLogScanHelperPath = "/usr/local/libexec/noderax-agent-operational-log-scan"
	linuxOperationalLogScanRequestPath = "/var/lib/noderax-agent/operational-log-scan-request.json"
	linuxTaskRootHelperPath          = "/usr/local/libexec/noderax-agent-task-root"
	linuxTaskRootRequestPath         = "/var/lib/noderax-agent/task-root-request.txt"
	linuxAgentServiceName            = "noderax-agent.service"
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
	RunAsRoot      bool              `json:"runAsRoot,omitempty"`
	RootScope      string            `json:"rootScope,omitempty"`
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

type agentUpdatePayload struct {
	TargetVersion string `json:"targetVersion"`
	TargetID      string `json:"targetId"`
	Rollback      bool   `json:"rollback,omitempty"`
}

type logScanPayload struct {
	Mode           string `json:"mode"`
	SourcePresetID string `json:"sourcePresetId"`
	RunAsRoot      bool   `json:"runAsRoot,omitempty"`
	RootScope      string `json:"rootScope,omitempty"`
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
	cmd := exec.CommandContext(ctx, name, args...)
	prepareCommand(cmd)
	return &execCommandRunner{cmd: cmd}
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
	defaultTimeout              time.Duration
	goos                        string
	lookPath                    func(string) (string, error)
	executablePath              func() (string, error)
	fileExists                  func(string) bool
	privilegedUpdateRequestPath string
	packageMutationRequestPath  string
	operationalLogScanRequestPath string
	taskRootRequestPath         string
	newCommand                  func(context.Context, string, ...string) commandRunner
	rootScopeChecker            func(string) bool
}

func NewShellExecutor(defaultTimeout time.Duration) *ShellExecutor {
	return &ShellExecutor{
		defaultTimeout: defaultTimeout,
		goos:           runtime.GOOS,
		lookPath:       exec.LookPath,
		executablePath: os.Executable,
		fileExists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		privilegedUpdateRequestPath: linuxPrivilegedUpdateRequestPath,
		packageMutationRequestPath:  linuxPackageMutationRequestPath,
		operationalLogScanRequestPath: linuxOperationalLogScanRequestPath,
		taskRootRequestPath:         linuxTaskRootRequestPath,
		newCommand:                  newExecCommandRunner,
	}
}

func (e *ShellExecutor) SetRootScopeChecker(checker func(string) bool) {
	e.rootScopeChecker = checker
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
		mx.Lock()
		if outBuilder.Len() < 1024*1024*2 { // 2MB limit
			if stream == "stderr" {
				outBuilder.WriteString("[stderr] ")
			}
			outBuilder.WriteString(line)
			outBuilder.WriteByte('\n')
		}
		mx.Unlock()

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
	case TaskTypeAgentUpdate:
		return e.agentUpdateCommand(task.Payload)
	case TaskTypeLogScan:
		return e.logScanCommand(task.Payload)
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

func (e *ShellExecutor) agentUpdateCommand(payload json.RawMessage) (commandSpec, error) {
	if err := e.ensureLinuxTaskSupport(); err != nil {
		return commandSpec{}, err
	}

	var parsed agentUpdatePayload
	if err := decodePayload(payload, &parsed, TaskTypeAgentUpdate); err != nil {
		return commandSpec{}, err
	}

	targetVersion := strings.TrimSpace(parsed.TargetVersion)
	if targetVersion == "" {
		return commandSpec{}, fmt.Errorf(
			"%w: agent.update payload requires targetVersion",
			ErrInvalidTaskPayload,
		)
	}

	targetID := strings.TrimSpace(parsed.TargetID)
	if targetID == "" {
		return commandSpec{}, fmt.Errorf(
			"%w: agent.update payload requires targetId",
			ErrInvalidTaskPayload,
		)
	}

	agentPath, err := e.lookPath("noderax-agent")
	if err != nil {
		agentPath, err = e.executablePath()
		if err != nil {
			return commandSpec{}, fmt.Errorf(
				"%w: could not resolve the managed noderax-agent binary",
				ErrUnsupportedExecutionEnvironment,
			)
		}
	}

	args := []string{
		"update",
		"--target-version",
		targetVersion,
		"--target-id",
		targetID,
	}
	if parsed.Rollback {
		args = append(args, "--rollback")
	}

	if e.goos == "linux" && e.fileExists(linuxPrivilegedUpdateHelperPath) {
		if err := writeManagedUpdateRequest(e.privilegedUpdateRequestPath, parsed); err != nil {
			return commandSpec{}, fmt.Errorf(
				"%w: write privileged update request: %v",
				ErrUnsupportedExecutionEnvironment,
				err,
			)
		}

		commandName, commandArgs, err := e.wrapWithSudo(
			linuxPrivilegedUpdateHelperPath,
			nil,
		)
		if err != nil {
			return commandSpec{}, err
		}

		return commandSpec{
			name:         commandName,
			args:         commandArgs,
			startMessage: fmt.Sprintf("handing off agent update to %s", targetVersion),
			parseResult: func(string, func(string, string)) any {
				return map[string]any{
					"status":        "handoff",
					"targetId":      targetID,
					"targetVersion": targetVersion,
					"rollback":      parsed.Rollback,
				}
			},
		}, nil
	}

	commandName, commandArgs, err := e.wrapWithSudo(agentPath, args)
	if err != nil {
		return commandSpec{}, err
	}

	return commandSpec{
		name:         commandName,
		args:         commandArgs,
		startMessage: fmt.Sprintf("handing off agent update to %s", targetVersion),
		parseResult: func(string, func(string, string)) any {
			return map[string]any{
				"status":        "handoff",
				"targetId":      targetID,
				"targetVersion": targetVersion,
				"rollback":      parsed.Rollback,
			}
		},
	}, nil
}

func (e *ShellExecutor) logScanCommand(payload json.RawMessage) (commandSpec, error) {
	if err := e.ensureLinuxTaskSupport(); err != nil {
		return commandSpec{}, err
	}

	var parsed logScanPayload
	if err := decodePayload(payload, &parsed, TaskTypeLogScan); err != nil {
		return commandSpec{}, err
	}

	if strings.TrimSpace(parsed.Mode) == "" {
		return commandSpec{}, fmt.Errorf("%w: log.scan payload requires mode", ErrInvalidTaskPayload)
	}
	if strings.TrimSpace(parsed.SourcePresetID) == "" {
		return commandSpec{}, fmt.Errorf("%w: log.scan payload requires sourcePresetId", ErrInvalidTaskPayload)
	}

	agentPath, err := e.lookPath("noderax-agent")
	if err != nil {
		agentPath, err = e.executablePath()
		if err != nil {
			return commandSpec{}, fmt.Errorf(
				"%w: could not resolve the managed noderax-agent binary",
				ErrUnsupportedExecutionEnvironment,
			)
		}
	}

	requestPath, err := writeJSONTempRequest("noderax-agent-log-scan-*.json", payload)
	if err != nil {
		return commandSpec{}, fmt.Errorf("%w: write log scan request: %v", ErrUnsupportedExecutionEnvironment, err)
	}

	commandName := agentPath
	commandArgs := []string{"log-scan", "--request", requestPath}

	if parsed.RunAsRoot {
		effectiveScope := strings.TrimSpace(parsed.RootScope)
		if effectiveScope == "" || effectiveScope == "task" {
			effectiveScope = "operational"
		}
		if effectiveScope != "operational" {
			return commandSpec{}, fmt.Errorf("%w: log.scan root execution requires operational scope", ErrInvalidTaskPayload)
		}
		if e.rootScopeChecker != nil && !e.rootScopeChecker(effectiveScope) {
			return commandSpec{}, fmt.Errorf("%w: current root access profile does not allow %s scope", ErrUnsupportedExecutionEnvironment, effectiveScope)
		}

		if e.goos == "linux" && e.fileExists(linuxOperationalLogScanHelperPath) {
			if err := writeOperationalLogScanRequest(e.operationalLogScanRequestPath, payload); err != nil {
				return commandSpec{}, fmt.Errorf("%w: write operational log scan request: %v", ErrUnsupportedExecutionEnvironment, err)
			}
			commandName, commandArgs, err = e.wrapWithSudo(linuxOperationalLogScanHelperPath, nil)
		} else {
			commandName, commandArgs, err = e.wrapWithSudo(commandName, commandArgs)
		}
		if err != nil {
			return commandSpec{}, err
		}
	}

	return commandSpec{
		name:         commandName,
		args:         commandArgs,
		startMessage: fmt.Sprintf("running log.scan for preset %s via %s", parsed.SourcePresetID, commandName),
		parseResult:  parseLogScanResult,
	}, nil
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
	if parsed.RunAsRoot {
		if strings.TrimSpace(parsed.RootScope) == "" {
			return commandSpec{}, fmt.Errorf("%w: shell.exec root execution requires rootScope", ErrInvalidTaskPayload)
		}
		if e.rootScopeChecker != nil && !e.rootScopeChecker(parsed.RootScope) {
			return commandSpec{}, fmt.Errorf("%w: current root access profile does not allow %s scope", ErrUnsupportedExecutionEnvironment, parsed.RootScope)
		}
		if parsed.RootScope == "operational" {
			commandName, args, err = e.buildOperationalRootCommand(parsed.Command)
			if err != nil {
				return commandSpec{}, err
			}
		} else {
			if e.goos == "linux" && e.fileExists(linuxTaskRootHelperPath) {
				if err := writeTaskRootRequest(e.taskRootRequestPath, parsed.Command); err != nil {
					return commandSpec{}, fmt.Errorf("%w: write task root request: %v", ErrUnsupportedExecutionEnvironment, err)
				}
				commandName, args, err = e.wrapWithSudo(linuxTaskRootHelperPath, nil)
			} else {
				commandName, args, err = e.wrapWithSudo(commandName, args)
			}
			if err != nil {
				return commandSpec{}, err
			}
		}
	}

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
			env:          map[string]string{"COLUMNS": "100000", "PAGER": "cat"},
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
		env:          map[string]string{"COLUMNS": "10000", "PAGER": "cat"},
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
		env:          map[string]string{"COLUMNS": "10000", "PAGER": "cat"},
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
	commandName, commandArgs, err := e.wrapPackageMutationCommand("install", aptGetPath, args, packages)
	if err != nil {
		return commandSpec{}, err
	}
	return commandSpec{
		name:         commandName,
		args:         commandArgs,
		env:          map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		startMessage: fmt.Sprintf("running %s", formatCommandForLog(commandName, commandArgs)),
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
	commandName, commandArgs, err := e.wrapPackageMutationCommand(action, aptGetPath, args, packages)
	if err != nil {
		return commandSpec{}, err
	}
	return commandSpec{
		name:         commandName,
		args:         commandArgs,
		env:          map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		startMessage: fmt.Sprintf("running %s", formatCommandForLog(commandName, commandArgs)),
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
	commandName, commandArgs, err := e.wrapPackageMutationCommand("purge", aptGetPath, args, packages)
	if err != nil {
		return commandSpec{}, err
	}
	return commandSpec{
		name:         commandName,
		args:         commandArgs,
		env:          map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		startMessage: fmt.Sprintf("running %s", formatCommandForLog(commandName, commandArgs)),
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

func (e *ShellExecutor) wrapWithSudo(
	commandName string,
	args []string,
) (string, []string, error) {
	if os.Geteuid() == 0 {
		return commandName, args, nil
	}

	sudoPath, err := e.lookPath("sudo")
	if err != nil {
		return commandName, args, nil
	}

	return sudoPath, append([]string{"-n", commandName}, args...), nil
}

func (e *ShellExecutor) wrapPackageMutationCommand(
	operation string,
	commandName string,
	args []string,
	packages []string,
) (string, []string, error) {
	if e.goos == "linux" && e.fileExists(linuxPackageMutationHelperPath) {
		if err := writePackageMutationRequest(e.packageMutationRequestPath, operation, packages); err != nil {
			return "", nil, fmt.Errorf("%w: write package mutation request: %v", ErrUnsupportedExecutionEnvironment, err)
		}
		return e.wrapWithSudo(linuxPackageMutationHelperPath, nil)
	}

	return e.wrapWithSudo(commandName, args)
}

func (e *ShellExecutor) buildOperationalRootCommand(
	command string,
) (string, []string, error) {
	normalizedCommand := strings.Join(strings.Fields(strings.TrimSpace(command)), " ")

	switch normalizedCommand {
	case "reboot":
		rebootPath, err := e.requireBinary("reboot")
		if err != nil {
			return "", nil, err
		}
		return e.wrapWithSudo(rebootPath, nil)
	case "systemctl restart noderax-agent", "systemctl restart noderax-agent.service":
		systemctlPath, err := e.requireBinary("systemctl")
		if err != nil {
			return "", nil, err
		}
		return e.wrapWithSudo(systemctlPath, []string{"restart", linuxAgentServiceName})
	case "apt-get update":
		aptGetPath, err := e.requireBinary("apt-get")
		if err != nil {
			return "", nil, err
		}
		if e.goos == "linux" && e.fileExists(linuxPackageMutationHelperPath) {
			if err := writePackageMutationRequest(e.packageMutationRequestPath, "update", nil); err != nil {
				return "", nil, fmt.Errorf("%w: write package mutation request: %v", ErrUnsupportedExecutionEnvironment, err)
			}
			return e.wrapWithSudo(linuxPackageMutationHelperPath, nil)
		}
		return e.wrapWithSudo(aptGetPath, []string{"update"})
	default:
		return "", nil, fmt.Errorf(
			"%w: unsupported operational root command %q",
			ErrInvalidTaskPayload,
			command,
		)
	}
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
	shell := payload.Shell
	if payload.RunAsRoot {
		shell = "/bin/sh"
	}
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

func writePackageMutationRequest(path string, operation string, packages []string) error {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "" {
		return fmt.Errorf("request path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil {
		return fmt.Errorf("create package mutation request directory: %w", err)
	}

	file, err := os.CreateTemp(filepath.Dir(cleanPath), ".noderax-agent-package-mutation-*.txt")
	if err != nil {
		return fmt.Errorf("create package mutation request file: %w", err)
	}

	tempPath := file.Name()
	writeErr := false
	if _, err := file.WriteString(strings.TrimSpace(operation) + "\n"); err != nil {
		writeErr = true
		file.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("write package mutation operation: %w", err)
	}

	for _, pkg := range packages {
		name := strings.TrimSpace(pkg)
		if name == "" {
			continue
		}
		if _, err := file.WriteString(name + "\n"); err != nil {
			writeErr = true
			file.Close()
			_ = os.Remove(tempPath)
			return fmt.Errorf("write package mutation request package: %w", err)
		}
	}

	if err := file.Chmod(0o600); err != nil {
		file.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("chmod package mutation request file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close package mutation request file: %w", err)
	}

	if err := os.Rename(tempPath, cleanPath); err != nil {
		_ = os.Remove(tempPath)
		if writeErr {
			return fmt.Errorf("replace package mutation request file after write failure: %w", err)
		}
		return fmt.Errorf("replace package mutation request file: %w", err)
	}

	return nil
}

func writeTaskRootRequest(path string, command string) error {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "" {
		return fmt.Errorf("request path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil {
		return fmt.Errorf("create task root request directory: %w", err)
	}

	tempFile, err := os.CreateTemp(filepath.Dir(cleanPath), ".noderax-agent-task-root-*.txt")
	if err != nil {
		return fmt.Errorf("create task root request file: %w", err)
	}

	tempPath := tempFile.Name()
	if _, err := tempFile.WriteString(command); err != nil {
		tempFile.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("write task root request file: %w", err)
	}
	if err := tempFile.Chmod(0o600); err != nil {
		tempFile.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("chmod task root request file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close task root request file: %w", err)
	}

	if err := os.Rename(tempPath, cleanPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace task root request file: %w", err)
	}

	return nil
}

func writeOperationalLogScanRequest(path string, payload json.RawMessage) error {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "" {
		return fmt.Errorf("request path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil {
		return fmt.Errorf("create operational log scan request directory: %w", err)
	}

	file, err := os.CreateTemp(filepath.Dir(cleanPath), ".noderax-agent-operational-log-scan-*.json")
	if err != nil {
		return fmt.Errorf("create operational log scan request file: %w", err)
	}

	tempPath := file.Name()
	if _, err := file.Write(payload); err != nil {
		file.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("write operational log scan request file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("chmod operational log scan request file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close operational log scan request file: %w", err)
	}

	if err := os.Rename(tempPath, cleanPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace operational log scan request file: %w", err)
	}

	return nil
}

func writeManagedUpdateRequest(path string, payload agentUpdatePayload) error {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "" {
		return fmt.Errorf("request path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil {
		return fmt.Errorf("create update request directory: %w", err)
	}

	file, err := os.CreateTemp(filepath.Dir(cleanPath), ".noderax-agent-update-request-*.json")
	if err != nil {
		return fmt.Errorf("create update request file: %w", err)
	}

	tempPath := file.Name()
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		file.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("write update request file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("chmod update request file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close update request file: %w", err)
	}

	if err := os.Rename(tempPath, cleanPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace update request file: %w", err)
	}

	return nil
}

func writeJSONTempRequest(pattern string, payload json.RawMessage) (string, error) {
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("create request file: %w", err)
	}

	tempPath := file.Name()
	if _, err := file.Write(payload); err != nil {
		file.Close()
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("write request file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("chmod request file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("close request file: %w", err)
	}

	return tempPath, nil
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

func parseLogScanResult(output string, onLog func(string, string)) any {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		emitLog(onLog, "system", "error: log.scan output is empty")
		return nil
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		emitLog(onLog, "system", fmt.Sprintf("error: failed to parse log.scan JSON result: %v", err))
		return nil
	}

	return parsed
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
