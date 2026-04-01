package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

func TestShellExecutorTimeoutFor(t *testing.T) {
	t.Parallel()

	executor := NewShellExecutor(5 * time.Minute)

	tests := []struct {
		name string
		task api.Task
		want time.Duration
	}{
		{
			name: "top level timeout wins for shell task",
			task: api.Task{
				Type:           TaskTypeShellExec,
				TimeoutSeconds: 45,
				Payload:        mustJSON(t, ShellExecPayload{Command: "echo hello", TimeoutSeconds: 10}),
			},
			want: 45 * time.Second,
		},
		{
			name: "payload timeout seconds",
			task: api.Task{
				Type:    TaskTypeShellExec,
				Payload: mustJSON(t, ShellExecPayload{Command: "echo hello", TimeoutSeconds: 90}),
			},
			want: 90 * time.Second,
		},
		{
			name: "payload duration string",
			task: api.Task{
				Type:    TaskTypeShellExec,
				Payload: mustJSON(t, ShellExecPayload{Command: "echo hello", Timeout: "2m30s"}),
			},
			want: 150 * time.Second,
		},
		{
			name: "package task uses top level timeout",
			task: api.Task{
				Type:           TaskTypePackageInstall,
				TimeoutSeconds: 12,
				Payload:        mustJSON(t, packageMutationPayload{Package: "nginx"}),
			},
			want: 12 * time.Second,
		},
		{
			name: "default timeout fallback",
			task: api.Task{
				Type:    TaskTypePackageSearch,
				Payload: mustJSON(t, packageSearchPayload{Query: "nginx"}),
			},
			want: 5 * time.Minute,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := executor.TimeoutFor(tt.task)
			if got != tt.want {
				t.Fatalf("timeout mismatch: got %s want %s", got, tt.want)
			}
		})
	}
}

func TestShellExecutorExecuteBuildsExpectedCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		task            api.Task
		goos            string
		lookPathResults map[string]string
		helperExists    bool
		wantName        string
		wantArgs        []string
		wantDir         string
		wantEnv         map[string]string
	}{
		{
			name: "shell exec keeps shell integration",
			task: api.Task{
				Type: TaskTypeShellExec,
				Payload: mustJSON(t, ShellExecPayload{
					Command:     "echo hello",
					Shell:       "/bin/bash",
					WorkingDir:  "/tmp/work",
					Environment: map[string]string{"FOO": "bar"},
				}),
			},
			goos:     "linux",
			wantName: "/bin/bash",
			wantArgs: []string{"-lc", "echo hello"},
			wantDir:  "/tmp/work",
			wantEnv:  map[string]string{"FOO": "bar"},
		},
		{
			name: "agent update prefers dedicated helper when installed",
			task: api.Task{
				Type:    TaskTypeAgentUpdate,
				Payload: mustJSON(t, agentUpdatePayload{TargetVersion: "1.0.0", TargetID: "target-1"}),
			},
			goos: "linux",
			lookPathResults: map[string]string{
				"noderax-agent": "/usr/local/bin/noderax-agent",
				"sudo":          "/usr/bin/sudo",
			},
			helperExists: true,
			wantName:     "/usr/bin/sudo",
			wantArgs: []string{
				"-n",
				linuxPrivilegedUpdateHelperPath,
				"--target-version",
				"1.0.0",
				"--target-id",
				"target-1",
			},
		},
		{
			name: "package list prefers dpkg",
			task: api.Task{
				Type: TaskTypePackageList,
			},
			goos:            "linux",
			lookPathResults: map[string]string{"dpkg": "/usr/bin/dpkg"},
			wantName:        "/usr/bin/dpkg",
			wantArgs:        []string{"-l"},
		},
		{
			name: "package list falls back to apt",
			task: api.Task{
				Type:    TaskTypePackageList,
				Payload: json.RawMessage(`{"ignored":"payload"}`),
			},
			goos:            "linux",
			lookPathResults: map[string]string{"apt": "/usr/bin/apt"},
			wantName:        "/usr/bin/apt",
			wantArgs:        []string{"list", "--installed"},
		},
		{
			name: "package search tokenizes query",
			task: api.Task{
				Type:    TaskTypePackageSearch,
				Payload: mustJSON(t, packageSearchPayload{Query: "nginx stable"}),
			},
			goos:            "linux",
			lookPathResults: map[string]string{"apt": "/usr/bin/apt"},
			wantName:        "/usr/bin/apt",
			wantArgs:        []string{"search", "nginx", "stable"},
		},
		{
			name: "package install supports singular package and env",
			task: api.Task{
				Type:    TaskTypePackageInstall,
				Payload: mustJSON(t, packageMutationPayload{Package: "nginx"}),
			},
			goos:            "linux",
			lookPathResults: map[string]string{"apt-get": "/usr/bin/apt-get"},
			wantName:        "/usr/bin/apt-get",
			wantArgs:        []string{"install", "-y", "--", "nginx"},
			wantEnv:         map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		},
		{
			name: "package remove purges multiple packages",
			task: api.Task{
				Type:    TaskTypePackageRemove,
				Payload: mustJSON(t, packageMutationPayload{Packages: []string{"nginx", "curl"}, Purge: true}),
			},
			goos:            "linux",
			lookPathResults: map[string]string{"apt-get": "/usr/bin/apt-get"},
			wantName:        "/usr/bin/apt-get",
			wantArgs:        []string{"purge", "-y", "--", "nginx", "curl"},
			wantEnv:         map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		},
		{
			name: "package remove without purge removes package",
			task: api.Task{
				Type:    TaskTypePackageRemove,
				Payload: mustJSON(t, packageMutationPayload{Package: "nginx"}),
			},
			goos:            "linux",
			lookPathResults: map[string]string{"apt-get": "/usr/bin/apt-get"},
			wantName:        "/usr/bin/apt-get",
			wantArgs:        []string{"remove", "-y", "--", "nginx"},
			wantEnv:         map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			executor := NewShellExecutor(5 * time.Minute)
			executor.goos = tt.goos
			executor.lookPath = fakeLookPath(tt.lookPathResults)
			executor.fileExists = func(path string) bool {
				return tt.helperExists && path == linuxPrivilegedUpdateHelperPath
			}

			recorder := &recordingCommandFactory{
				runner: &fakeCommandRunner{
					stdoutText: "line 1\nline 2\n",
					stderrText: "warn line\n",
				},
			}
			executor.newCommand = recorder.factory

			logs := make([]string, 0, 6)
			result, err := executor.Execute(context.Background(), tt.task, func(stream, line string) {
				logs = append(logs, stream+":"+line)
			})
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if result.ExitCode != 0 {
				t.Fatalf("unexpected exit code: got %d want 0", result.ExitCode)
			}
			if recorder.calls != 1 {
				t.Fatalf("expected one command invocation, got %d", recorder.calls)
			}
			if recorder.name != tt.wantName {
				t.Fatalf("command name mismatch: got %q want %q", recorder.name, tt.wantName)
			}
			if !reflect.DeepEqual(recorder.args, tt.wantArgs) {
				t.Fatalf("command args mismatch: got %v want %v", recorder.args, tt.wantArgs)
			}
			if recorder.runner.dir != tt.wantDir {
				t.Fatalf("working dir mismatch: got %q want %q", recorder.runner.dir, tt.wantDir)
			}
			for key, value := range tt.wantEnv {
				if !hasEnv(recorder.runner.env, key, value) {
					t.Fatalf("expected env %s=%s in %v", key, value, recorder.runner.env)
				}
			}
			if !containsLog(logs, "system:running ") && !containsLog(logs, "system:handing off agent update to ") {
				t.Fatalf("expected system start log, got %v", logs)
			}
			if !containsLog(logs, "stdout:line 1") {
				t.Fatalf("expected stdout log, got %v", logs)
			}
			if !containsLog(logs, "stderr:warn line") {
				t.Fatalf("expected stderr log, got %v", logs)
			}
			if !containsLog(logs, "system:command finished with exit code 0") {
				t.Fatalf("expected completion log, got %v", logs)
			}
		})
	}
}

func TestShellExecutorExecuteRejectsInvalidPackagePayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		task api.Task
	}{
		{
			name: "search requires query",
			task: api.Task{
				Type:    TaskTypePackageSearch,
				Payload: mustJSON(t, packageSearchPayload{Query: "   "}),
			},
		},
		{
			name: "search rejects option like terms",
			task: api.Task{
				Type:    TaskTypePackageSearch,
				Payload: mustJSON(t, packageSearchPayload{Query: "-o Debug::pkgProblemResolver=yes"}),
			},
		},
		{
			name: "install requires package list",
			task: api.Task{
				Type:    TaskTypePackageInstall,
				Payload: mustJSON(t, packageMutationPayload{}),
			},
		},
		{
			name: "remove rejects empty package entries",
			task: api.Task{
				Type:    TaskTypePackageRemove,
				Payload: mustJSON(t, packageMutationPayload{Packages: []string{"nginx", " "}}),
			},
		},
		{
			name: "install rejects option like package names",
			task: api.Task{
				Type:    TaskTypePackageInstall,
				Payload: mustJSON(t, packageMutationPayload{Package: "-y"}),
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			executor := NewShellExecutor(5 * time.Minute)
			executor.goos = "linux"
			executor.lookPath = fakeLookPath(map[string]string{
				"apt":     "/usr/bin/apt",
				"apt-get": "/usr/bin/apt-get",
				"dpkg":    "/usr/bin/dpkg",
			})

			recorder := &recordingCommandFactory{runner: &fakeCommandRunner{}}
			executor.newCommand = recorder.factory

			logs := make([]string, 0, 2)
			_, err := executor.Execute(context.Background(), tt.task, func(stream, line string) {
				logs = append(logs, stream+":"+line)
			})
			if !errors.Is(err, ErrInvalidTaskPayload) {
				t.Fatalf("expected ErrInvalidTaskPayload, got %v", err)
			}
			if recorder.calls != 0 {
				t.Fatalf("expected command not to start, got %d calls", recorder.calls)
			}
			if len(logs) == 0 || !strings.HasPrefix(logs[0], "system:") {
				t.Fatalf("expected system error log, got %v", logs)
			}
		})
	}
}

func TestShellExecutorExecuteRejectsUnsupportedEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		task            api.Task
		goos            string
		lookPathResults map[string]string
	}{
		{
			name: "package tasks require linux",
			task: api.Task{
				Type:    TaskTypePackageInstall,
				Payload: mustJSON(t, packageMutationPayload{Package: "nginx"}),
			},
			goos:            "darwin",
			lookPathResults: map[string]string{"apt-get": "/usr/bin/apt-get"},
		},
		{
			name: "install requires apt-get binary",
			task: api.Task{
				Type:    TaskTypePackageInstall,
				Payload: mustJSON(t, packageMutationPayload{Package: "nginx"}),
			},
			goos:            "linux",
			lookPathResults: map[string]string{},
		},
		{
			name: "package list requires apt tooling",
			task: api.Task{
				Type: TaskTypePackageList,
			},
			goos:            "linux",
			lookPathResults: map[string]string{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			executor := NewShellExecutor(5 * time.Minute)
			executor.goos = tt.goos
			executor.lookPath = fakeLookPath(tt.lookPathResults)

			recorder := &recordingCommandFactory{runner: &fakeCommandRunner{}}
			executor.newCommand = recorder.factory

			_, err := executor.Execute(context.Background(), tt.task, nil)
			if !errors.Is(err, ErrUnsupportedExecutionEnvironment) {
				t.Fatalf("expected ErrUnsupportedExecutionEnvironment, got %v", err)
			}
			if recorder.calls != 0 {
				t.Fatalf("expected command not to start, got %d calls", recorder.calls)
			}
		})
	}
}

func TestShellExecutorExecutePropagatesExitCodeAndLogs(t *testing.T) {
	t.Parallel()

	executor := NewShellExecutor(5 * time.Minute)
	executor.goos = "linux"
	executor.lookPath = fakeLookPath(map[string]string{"apt-get": "/usr/bin/apt-get"})

	var recordedName string
	var recordedArgs []string
	executor.newCommand = func(ctx context.Context, name string, args ...string) commandRunner {
		recordedName = name
		recordedArgs = append([]string(nil), args...)
		return newHelperCommandRunner(ctx, "installed nginx\n", "apt warning\n", 7)
	}

	logs := make([]string, 0, 4)
	result, err := executor.Execute(context.Background(), api.Task{
		Type:    TaskTypePackageInstall,
		Payload: mustJSON(t, packageMutationPayload{Package: "nginx"}),
	}, func(stream, line string) {
		logs = append(logs, stream+":"+line)
	})

	if err == nil {
		t.Fatal("expected command error, got nil")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %T (%v)", err, err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code mismatch: got %d want 7", result.ExitCode)
	}
	if recordedName != "/usr/bin/apt-get" {
		t.Fatalf("command name mismatch: got %q", recordedName)
	}
	wantArgs := []string{"install", "-y", "--", "nginx"}
	if !reflect.DeepEqual(recordedArgs, wantArgs) {
		t.Fatalf("command args mismatch: got %v want %v", recordedArgs, wantArgs)
	}
	if !containsLog(logs, "stdout:installed nginx") {
		t.Fatalf("expected stdout log, got %v", logs)
	}
	if !containsLog(logs, "stderr:apt warning") {
		t.Fatalf("expected stderr log, got %v", logs)
	}
	if !containsLog(logs, "system:command finished with exit code 7") {
		t.Fatalf("expected completion log, got %v", logs)
	}
}

func TestCommandHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	if _, err := io.WriteString(os.Stdout, os.Getenv("HELPER_STDOUT")); err != nil {
		os.Exit(90)
	}
	if _, err := io.WriteString(os.Stderr, os.Getenv("HELPER_STDERR")); err != nil {
		os.Exit(91)
	}

	code, err := strconv.Atoi(os.Getenv("HELPER_EXIT_CODE"))
	if err != nil {
		os.Exit(92)
	}
	os.Exit(code)
}

type fakeCommandRunner struct {
	env        []string
	dir        string
	stdoutText string
	stderrText string
	startErr   error
	waitErr    error
}

func (f *fakeCommandRunner) SetEnv(env []string) {
	f.env = append([]string(nil), env...)
}

func (f *fakeCommandRunner) SetDir(dir string) {
	f.dir = dir
}

func (f *fakeCommandRunner) StdoutPipe() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.stdoutText)), nil
}

func (f *fakeCommandRunner) StderrPipe() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.stderrText)), nil
}

func (f *fakeCommandRunner) Start() error {
	return f.startErr
}

func (f *fakeCommandRunner) Wait() error {
	return f.waitErr
}

type recordingCommandFactory struct {
	calls  int
	name   string
	args   []string
	runner *fakeCommandRunner
}

func (r *recordingCommandFactory) factory(_ context.Context, name string, args ...string) commandRunner {
	r.calls++
	r.name = name
	r.args = append([]string(nil), args...)
	return r.runner
}

type helperCommandRunner struct {
	*execCommandRunner
	extraEnv []string
}

func newHelperCommandRunner(ctx context.Context, stdout, stderr string, exitCode int) commandRunner {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCommandHelperProcess", "--")
	return &helperCommandRunner{
		execCommandRunner: &execCommandRunner{cmd: cmd},
		extraEnv: []string{
			"GO_WANT_HELPER_PROCESS=1",
			"HELPER_STDOUT=" + stdout,
			"HELPER_STDERR=" + stderr,
			fmt.Sprintf("HELPER_EXIT_CODE=%d", exitCode),
		},
	}
}

func (r *helperCommandRunner) SetEnv(env []string) {
	env = append(append([]string(nil), env...), r.extraEnv...)
	r.execCommandRunner.SetEnv(env)
}

func fakeLookPath(results map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if path, ok := results[name]; ok {
			return path, nil
		}
		return "", exec.ErrNotFound
	}
}

func containsLog(logs []string, want string) bool {
	for _, entry := range logs {
		if strings.Contains(entry, want) {
			return true
		}
	}
	return false
}

func hasEnv(env []string, key, wantValue string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) && strings.TrimPrefix(entry, prefix) == wantValue {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, payload any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	return data
}
