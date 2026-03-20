package agentctl

import (
	"bytes"
	"context"
	"os"
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

func writeTempServiceDefinition(t *testing.T, content string) string {
	t.Helper()

	path := t.TempDir() + "/service-definition"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp service definition: %v", err)
	}

	return path
}
