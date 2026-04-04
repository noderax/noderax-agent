package agentctl

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noderax/noderax-agent/internal/config"
)

func TestApplyConfigValue(t *testing.T) {
	t.Parallel()

	cfg := config.Default()

	if err := applyConfigValue(&cfg, "api_url", "https://api.example.com"); err != nil {
		t.Fatalf("applyConfigValue api_url returned error: %v", err)
	}
	if err := applyConfigValue(&cfg, "task_timeout", "45s"); err != nil {
		t.Fatalf("applyConfigValue task_timeout returned error: %v", err)
	}
	if err := applyConfigValue(&cfg, "log_level", "debug"); err != nil {
		t.Fatalf("applyConfigValue log_level returned error: %v", err)
	}

	if cfg.APIURL != "https://api.example.com" {
		t.Fatalf("api url mismatch: got %q want %q", cfg.APIURL, "https://api.example.com")
	}
	if cfg.TaskTimeout != 45*time.Second {
		t.Fatalf("task timeout mismatch: got %s want %s", cfg.TaskTimeout, 45*time.Second)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("log level mismatch: got %q want %q", cfg.LogLevel, "debug")
	}
}

func TestApplyConfigValueRejectsUnknownKey(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	if err := applyConfigValue(&cfg, "unsupported_key", "value"); err == nil {
		t.Fatal("expected error for unsupported key, got nil")
	}
}

func TestRenderServiceUnit(t *testing.T) {
	t.Parallel()

	spec := platformSpec{
		BinaryPath:  linuxBinaryPath,
		WorkingDir:  linuxInstallDir,
		ServiceName: linuxServiceName,
	}
	unit := renderServiceUnit(spec, linuxConfigPath)

	if !strings.Contains(unit, "Environment=NODERAX_CONFIG_FILE="+linuxConfigPath) {
		t.Fatalf("service unit is missing config path: %s", unit)
	}
	if !strings.Contains(unit, "ExecStart="+linuxBinaryPath) {
		t.Fatalf("service unit is missing ExecStart: %s", unit)
	}
	if !strings.Contains(unit, "Restart=always") {
		t.Fatalf("service unit is missing restart policy: %s", unit)
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	t.Parallel()

	spec := platformSpec{
		BinaryPath:    macOSBinaryPath,
		WorkingDir:    macOSInstallDir,
		ServiceName:   macOSServiceName,
		LogStdoutPath: "/var/log/noderax-agent.log",
		LogStderrPath: "/var/log/noderax-agent.error.log",
	}

	plist := renderLaunchdPlist(spec, macOSConfigPath)

	if !strings.Contains(plist, "<string>"+macOSServiceName+"</string>") {
		t.Fatalf("launchd plist is missing service name: %s", plist)
	}
	if !strings.Contains(plist, "<string>"+macOSBinaryPath+"</string>") {
		t.Fatalf("launchd plist is missing binary path: %s", plist)
	}
	if !strings.Contains(plist, "<string>"+macOSConfigPath+"</string>") {
		t.Fatalf("launchd plist is missing config path: %s", plist)
	}
}

func TestRenderBaseSudoersIncludesExplicitRootProfileCommands(t *testing.T) {
	t.Parallel()

	spec := platformSpec{
		PrivilegedUpdateHelperPath: "/usr/local/libexec/noderax-agent-self-update",
		RootProfileHelperPath:      "/usr/local/libexec/noderax-agent-root-profile",
		ServiceUser:                "noderax",
	}

	sudoers := renderBaseSudoers(spec)

	if strings.Contains(sudoers, "apply *") {
		t.Fatalf("base sudoers must not use wildcard root profile command matching: %s", sudoers)
	}

	for _, snippet := range []string{
		"/usr/local/libexec/noderax-agent-self-update",
		"/usr/local/libexec/noderax-agent-root-profile apply off",
		"/usr/local/libexec/noderax-agent-root-profile apply operational",
		"/usr/local/libexec/noderax-agent-root-profile apply task",
		"/usr/local/libexec/noderax-agent-root-profile apply terminal",
		"/usr/local/libexec/noderax-agent-root-profile apply operational_task",
		"/usr/local/libexec/noderax-agent-root-profile apply operational_terminal",
		"/usr/local/libexec/noderax-agent-root-profile apply task_terminal",
		"/usr/local/libexec/noderax-agent-root-profile apply all",
		"noderax ALL=(root) NOPASSWD: /usr/local/libexec/noderax-agent-self-update",
	} {
		if !strings.Contains(sudoers, snippet) {
			t.Fatalf("expected sudoers to contain %q, got %s", snippet, sudoers)
		}
	}
}

func TestRenderRootProfileHelperSupportsCombinedProfiles(t *testing.T) {
	t.Parallel()

	spec := platformSpec{
		RootProfileHelperPath:     "/usr/local/libexec/noderax-agent-root-profile",
		ServiceUser:               "noderax",
		ServiceName:               "noderax-agent.service",
		RootAccessSudoersPath:     "/etc/sudoers.d/noderax-agent-root-access",
		PackageMutationHelperPath: "/usr/local/libexec/noderax-agent-package-mutation",
		TaskRootHelperPath:        "/usr/local/libexec/noderax-agent-task-root",
	}

	helper := renderRootProfileHelper(spec)

	for _, snippet := range []string{
		"usage: ${HELPER_PATH} apply <off|operational|task|terminal|operational_task|operational_terminal|task_terminal|all>",
		"operational_task)",
		"operational_terminal)",
		"task_terminal)",
		"append_operational_profile",
		"append_task_profile",
		"append_terminal_profile",
		"for shell_name in bash zsh sh; do",
		`append_terminal_command "$(command -v "${shell_name}" || true)"`,
		"/usr/bin/bash",
		"/usr/bin/zsh",
		"/usr/bin/sh",
	} {
		if !strings.Contains(helper, snippet) {
			t.Fatalf("expected root profile helper to contain %q, got %s", snippet, helper)
		}
	}
}

func TestConfigPathFromServiceDefinitionContent(t *testing.T) {
	t.Parallel()

	systemdUnit := renderServiceUnit(platformSpec{
		BinaryPath:  linuxBinaryPath,
		WorkingDir:  linuxInstallDir,
		ServiceName: linuxServiceName,
	}, linuxConfigPath)
	systemdPath := writeTempServiceDefinition(t, systemdUnit)

	if got := configPathFromServiceDefinition(systemdPath); got != linuxConfigPath {
		t.Fatalf("configPathFromServiceDefinition(systemd) = %q, want %q", got, linuxConfigPath)
	}

	launchdPlist := renderLaunchdPlist(platformSpec{
		BinaryPath:    macOSBinaryPath,
		WorkingDir:    macOSInstallDir,
		ServiceName:   macOSServiceName,
		LogStdoutPath: "/var/log/noderax-agent.log",
		LogStderrPath: "/var/log/noderax-agent.error.log",
	}, macOSConfigPath)
	launchdPath := writeTempServiceDefinition(t, launchdPlist)

	if got := configPathFromServiceDefinition(launchdPath); got != macOSConfigPath {
		t.Fatalf("configPathFromServiceDefinition(launchd) = %q, want %q", got, macOSConfigPath)
	}
}

func TestLoadPersistedRootAccessProfileReadsAppliedProfile(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "agent_identity.json")
	rootAccessStatePath := filepath.Join(stateDir, "root_access_state.json")

	content, err := json.Marshal(map[string]any{
		"appliedProfile": "terminal",
	})
	if err != nil {
		t.Fatalf("marshal root access state: %v", err)
	}
	if err := os.WriteFile(rootAccessStatePath, content, 0o600); err != nil {
		t.Fatalf("write root access state: %v", err)
	}

	profile, found, err := loadPersistedRootAccessProfile(statePath)
	if err != nil {
		t.Fatalf("loadPersistedRootAccessProfile returned error: %v", err)
	}
	if !found {
		t.Fatal("expected persisted root access profile to be found")
	}
	if profile != "terminal" {
		t.Fatalf("persisted root access profile mismatch: got %q want %q", profile, "terminal")
	}
}

func TestLoadPersistedRootAccessProfileSkipsMissingState(t *testing.T) {
	t.Parallel()

	profile, found, err := loadPersistedRootAccessProfile(
		filepath.Join(t.TempDir(), "agent_identity.json"),
	)
	if err != nil {
		t.Fatalf("loadPersistedRootAccessProfile returned error: %v", err)
	}
	if found {
		t.Fatalf("expected missing root access state to be ignored, got profile %q", profile)
	}
}

func TestHandlePrintsLogoForManagedCommands(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	cli := CLI{
		Stdout: &stdout,
		Stderr: &stdout,
	}

	handled, err := cli.Handle(context.Background(), []string{"config"})
	if !handled {
		t.Fatal("expected config command to be handled")
	}
	if err == nil {
		t.Fatal("expected usage error for missing config subcommand")
	}
	if !strings.Contains(stdout.String(), "→→") {
		t.Fatalf("expected logo to be printed, got %q", stdout.String())
	}
}

func TestHandleVersionCommandPrintsBuildMetadata(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	cli := CLI{
		Version:   "1.0.2",
		Commit:    "abc1234",
		BuildDate: "2026-04-02T10:30:00Z",
		Stdout:    &stdout,
		Stderr:    &stdout,
	}

	handled, err := cli.Handle(context.Background(), []string{"version"})
	if !handled {
		t.Fatal("expected version command to be handled")
	}
	if err != nil {
		t.Fatalf("version command returned error: %v", err)
	}

	output := stdout.String()
	for _, snippet := range []string{
		"Noderax Agent",
		"Version    : 1.0.2",
		"Commit     : abc1234",
		"Build date : 2026-04-02T10:30:00Z",
	} {
		if !strings.Contains(output, snippet) {
			t.Fatalf("expected output to contain %q, got %q", snippet, output)
		}
	}
}

func TestHandleVersionFlagPrintsBuildMetadata(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	cli := CLI{
		Version:   "1.0.2",
		Commit:    "abc1234",
		BuildDate: "2026-04-02T10:30:00Z",
		Stdout:    &stdout,
		Stderr:    &stdout,
	}

	handled, err := cli.Handle(context.Background(), []string{"--version"})
	if !handled {
		t.Fatal("expected --version to be handled")
	}
	if err != nil {
		t.Fatalf("--version returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Version    : 1.0.2") {
		t.Fatalf("expected version output, got %q", stdout.String())
	}
}

func writeTempServiceDefinition(t *testing.T, content string) string {
	t.Helper()

	path := t.TempDir() + "/service-definition"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp service definition: %v", err)
	}

	return path
}
