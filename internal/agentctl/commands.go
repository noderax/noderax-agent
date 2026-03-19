package agentctl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/noderax/noderax-agent/internal/agent"
	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/config"
)

const (
	serviceManagerSystemd = "systemd"
	serviceManagerLaunchd = "launchd"

	linuxInstallDir  = "/opt/noderax-agent"
	linuxBinaryPath  = linuxInstallDir + "/noderax-agent"
	linuxSymlinkPath = "/usr/local/bin/noderax-agent"
	linuxConfigPath  = "/etc/noderax-agent/config.json"
	linuxStatePath   = "/var/lib/noderax-agent/agent_identity.json"
	linuxServiceUnit = "/etc/systemd/system/noderax-agent.service"
	linuxServiceName = "noderax-agent.service"

	macOSInstallDir  = "/usr/local/lib/noderax-agent"
	macOSBinaryPath  = macOSInstallDir + "/noderax-agent"
	macOSSymlinkPath = "/usr/local/bin/noderax-agent"
	macOSConfigPath  = "/usr/local/etc/noderax-agent/config.json"
	macOSStatePath   = "/usr/local/var/lib/noderax-agent/agent_identity.json"
	macOSServiceUnit = "/Library/LaunchDaemons/com.noderax.agent.plist"
	macOSServiceName = "com.noderax.agent"
)

type platformSpec struct {
	Manager       string
	InstallDir    string
	BinaryPath    string
	SymlinkPath   string
	ConfigPath    string
	StatePath     string
	ServiceUnit   string
	ServiceName   string
	WorkingDir    string
	RequiresRoot  bool
	ServiceDomain string
	LogStdoutPath string
	LogStderrPath string
}

type CLI struct {
	Logger  *slog.Logger
	Version string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

func (c CLI) Handle(ctx context.Context, args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}

	switch args[0] {
	case "install":
		return true, c.Install(ctx)
	case "start", "stop", "restart", "status":
		return true, c.ServiceAction(ctx, args[0])
	case "config":
		return true, c.Config(ctx, args[1:])
	default:
		return false, nil
	}
}

func (c CLI) Install(ctx context.Context) error {
	spec, err := currentPlatformSpec()
	if err != nil {
		return err
	}
	if spec.RequiresRoot {
		if err := requireRoot(); err != nil {
			return err
		}
	}
	if err := ensureServiceManager(spec); err != nil {
		return err
	}

	cfg := config.Default()
	cfg.ConfigFile = managedConfigPath(spec)
	cfg.StateFile = spec.StatePath

	if existing, err := loadManagedConfig(spec); err == nil {
		cfg = existing
		cfg.ConfigFile = managedConfigPath(spec)
		cfg.StateFile = spec.StatePath
	}

	reader := bufio.NewReader(c.stdinOrDefault())
	apiURL, err := promptValue(reader, c.stdoutOrDefault(), "API URL", cfg.APIURL, true)
	if err != nil {
		return err
	}
	logLevel, err := promptValue(reader, c.stdoutOrDefault(), "Log level", cfg.LogLevel, false)
	if err != nil {
		return err
	}

	cfg.APIURL = apiURL
	if logLevel != "" {
		cfg.LogLevel = logLevel
	}
	cfg.ConfigFile = managedConfigPath(spec)
	cfg.StateFile = spec.StatePath
	cfg.EnrollmentToken = ""
	cfg.NodeID = ""
	cfg.AgentToken = ""

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.SaveFile(cfg.ConfigFile, cfg); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	executablePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(executablePath); err == nil {
		executablePath = resolved
	}

	if err := copyExecutable(executablePath, spec.BinaryPath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	if spec.SymlinkPath != "" {
		if err := ensureSymlink(spec.BinaryPath, spec.SymlinkPath); err != nil {
			return fmt.Errorf("create symlink: %w", err)
		}
	}

	switch spec.Manager {
	case serviceManagerSystemd:
		if err := writeServiceUnit(spec.ServiceUnit, renderServiceUnit(spec, cfg.ConfigFile)); err != nil {
			return fmt.Errorf("write service unit: %w", err)
		}
	case serviceManagerLaunchd:
		if err := writeServiceUnit(spec.ServiceUnit, renderLaunchdPlist(spec, cfg.ConfigFile)); err != nil {
			return fmt.Errorf("write launchd plist: %w", err)
		}
	default:
		return fmt.Errorf("unsupported service manager %q", spec.Manager)
	}

	client := api.NewClient(cfg.APIURL, cfg.RequestTimeout)
	if err := agent.RunInteractiveEnrollment(ctx, cfg, client, c.Logger, c.Version, c.stdinOrDefault(), c.stdoutOrDefault()); err != nil {
		return err
	}

	if err := c.enableAndStartService(ctx, spec); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(c.stdoutOrDefault(), "\nInstall completed.\n")
	_, _ = fmt.Fprintf(c.stdoutOrDefault(), "Service: %s\n", spec.ServiceName)
	_, _ = fmt.Fprintf(c.stdoutOrDefault(), "Binary: %s\n", spec.BinaryPath)
	_, _ = fmt.Fprintf(c.stdoutOrDefault(), "Config: %s\n", cfg.ConfigFile)
	_, _ = fmt.Fprintf(c.stdoutOrDefault(), "State: %s\n", cfg.StateFile)
	return nil
}

func (c CLI) ServiceAction(ctx context.Context, action string) error {
	spec, err := currentPlatformSpec()
	if err != nil {
		return err
	}

	if serviceActionRequiresRoot(spec, action) {
		if err := requireRoot(); err != nil {
			return err
		}
	}

	switch spec.Manager {
	case serviceManagerSystemd:
		args := []string{action, spec.ServiceName}
		if action == "status" {
			args = []string{"--no-pager", "--full", "status", spec.ServiceName}
		}
		return c.runSystemctl(ctx, args...)
	case serviceManagerLaunchd:
		return c.runLaunchdAction(ctx, spec, action)
	default:
		return fmt.Errorf("unsupported service manager %q", spec.Manager)
	}
}

func (c CLI) Config(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: noderax-agent config <show|set>")
	}

	spec, err := currentPlatformSpec()
	if err != nil {
		return err
	}

	switch args[0] {
	case "show":
		return c.showConfig(spec)
	case "set":
		if spec.RequiresRoot {
			if err := requireRoot(); err != nil {
				return err
			}
		}
		if len(args) != 3 {
			return fmt.Errorf("usage: noderax-agent config set <key> <value>")
		}
		return c.setConfig(ctx, spec, args[1], args[2])
	default:
		return fmt.Errorf("unsupported config command %q", args[0])
	}
}

func (c CLI) showConfig(spec platformSpec) error {
	path := managedConfigPath(spec)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file %s: %w", path, err)
	}

	_, _ = fmt.Fprintf(c.stdoutOrDefault(), "Config path: %s\n", path)
	_, _ = c.stdoutOrDefault().Write(data)
	_, _ = fmt.Fprintln(c.stdoutOrDefault())
	return nil
}

func (c CLI) setConfig(ctx context.Context, spec platformSpec, key, value string) error {
	cfg, err := loadManagedConfig(spec)
	if err != nil {
		return err
	}
	cfg.ConfigFile = managedConfigPath(spec)

	if err := applyConfigValue(&cfg, key, value); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.SaveFile(cfg.ConfigFile, cfg); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	_, _ = fmt.Fprintf(c.stdoutOrDefault(), "Updated %s in %s\n", key, cfg.ConfigFile)

	if _, err := os.Stat(spec.ServiceUnit); err == nil {
		switch spec.Manager {
		case serviceManagerSystemd:
			if err := c.runSystemctl(ctx, "daemon-reload"); err != nil {
				return err
			}
			if err := c.runSystemctl(ctx, "restart", spec.ServiceName); err != nil {
				return err
			}
		case serviceManagerLaunchd:
			if err := c.runLaunchdAction(ctx, spec, "restart"); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintln(c.stdoutOrDefault(), "Service restarted to apply the new configuration.")
	}

	return nil
}

func currentPlatformSpec() (platformSpec, error) {
	switch runtime.GOOS {
	case "linux":
		return platformSpec{
			Manager:       serviceManagerSystemd,
			InstallDir:    linuxInstallDir,
			BinaryPath:    linuxBinaryPath,
			SymlinkPath:   linuxSymlinkPath,
			ConfigPath:    linuxConfigPath,
			StatePath:     linuxStatePath,
			ServiceUnit:   linuxServiceUnit,
			ServiceName:   linuxServiceName,
			WorkingDir:    linuxInstallDir,
			RequiresRoot:  true,
			ServiceDomain: "system",
		}, nil
	case "darwin":
		return platformSpec{
			Manager:       serviceManagerLaunchd,
			InstallDir:    macOSInstallDir,
			BinaryPath:    macOSBinaryPath,
			SymlinkPath:   macOSSymlinkPath,
			ConfigPath:    macOSConfigPath,
			StatePath:     macOSStatePath,
			ServiceUnit:   macOSServiceUnit,
			ServiceName:   macOSServiceName,
			WorkingDir:    macOSInstallDir,
			RequiresRoot:  true,
			ServiceDomain: "system",
			LogStdoutPath: "/var/log/noderax-agent.log",
			LogStderrPath: "/var/log/noderax-agent.error.log",
		}, nil
	default:
		return platformSpec{}, fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}

func ensureServiceManager(spec platformSpec) error {
	switch spec.Manager {
	case serviceManagerSystemd:
		if _, err := exec.LookPath("systemctl"); err != nil {
			return fmt.Errorf("systemctl is required for install")
		}
	case serviceManagerLaunchd:
		if _, err := exec.LookPath("launchctl"); err != nil {
			return fmt.Errorf("launchctl is required for install")
		}
	default:
		return fmt.Errorf("unsupported service manager %q", spec.Manager)
	}

	return nil
}

func serviceActionRequiresRoot(spec platformSpec, action string) bool {
	if !spec.RequiresRoot {
		return false
	}
	if spec.Manager == serviceManagerSystemd && action == "status" {
		return false
	}
	return true
}

func applyConfigValue(cfg *config.Config, key, value string) error {
	switch key {
	case "api_url":
		cfg.APIURL = value
	case "enrollment_token":
		cfg.EnrollmentToken = value
	case "node_id":
		cfg.NodeID = value
	case "agent_token":
		cfg.AgentToken = value
	case "heartbeat_interval":
		return setDuration(&cfg.HeartbeatInterval, value)
	case "metrics_interval":
		return setDuration(&cfg.MetricsInterval, value)
	case "task_poll_interval":
		return setDuration(&cfg.TaskPollInterval, value)
	case "request_timeout":
		return setDuration(&cfg.RequestTimeout, value)
	case "task_timeout":
		return setDuration(&cfg.TaskTimeout, value)
	case "shutdown_timeout":
		return setDuration(&cfg.ShutdownTimeout, value)
	case "state_file":
		cfg.StateFile = value
	case "log_level":
		cfg.LogLevel = value
	default:
		return fmt.Errorf("unsupported config key %q", key)
	}

	return nil
}

func setDuration(target *time.Duration, value string) error {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", value, err)
	}
	*target = duration
	return nil
}

func loadManagedConfig(spec platformSpec) (config.Config, error) {
	return config.LoadFile(managedConfigPath(spec))
}

func managedConfigPath(spec platformSpec) string {
	if value := strings.TrimSpace(os.Getenv("NODERAX_CONFIG_FILE")); value != "" {
		return value
	}
	return spec.ConfigPath
}

func renderServiceUnit(spec platformSpec, configPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Noderax Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
Environment=NODERAX_CONFIG_FILE=%s
ExecStart=%s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, spec.WorkingDir, configPath, spec.BinaryPath)
}

func renderLaunchdPlist(spec platformSpec, configPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
      <string>%s</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
      <key>NODERAX_CONFIG_FILE</key>
      <string>%s</string>
    </dict>
    <key>WorkingDirectory</key>
    <string>%s</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
  </dict>
</plist>
`, spec.ServiceName, spec.BinaryPath, configPath, spec.WorkingDir, spec.LogStdoutPath, spec.LogStderrPath)
}

func copyExecutable(src, dst string) error {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil
	}

	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source executable %s: %w", src, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create binary directory for %s: %w", dst, err)
	}

	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source executable %s: %w", src, err)
	}
	defer sourceFile.Close()

	targetFile, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode()&0o755)
	if err != nil {
		return fmt.Errorf("open target executable %s: %w", dst, err)
	}
	defer targetFile.Close()

	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return fmt.Errorf("copy executable to %s: %w", dst, err)
	}

	return nil
}

func ensureSymlink(target, link string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return fmt.Errorf("create symlink directory for %s: %w", link, err)
	}

	if existingTarget, err := os.Readlink(link); err == nil {
		if existingTarget == target {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("remove existing symlink %s: %w", link, err)
		}
	} else if !os.IsNotExist(err) {
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("remove existing path %s: %w", link, err)
		}
	}

	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("create symlink %s -> %s: %w", link, target, err)
	}

	return nil
}

func writeServiceUnit(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create service directory for %s: %w", path, err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write service definition %s: %w", path, err)
	}

	return nil
}

func requireRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("this command must be run as root; try again with sudo")
	}
	return nil
}

func promptValue(reader *bufio.Reader, writer io.Writer, label, defaultValue string, required bool) (string, error) {
	prompt := label
	if defaultValue != "" {
		prompt += fmt.Sprintf(" [%s]", defaultValue)
	}
	prompt += ": "

	for {
		if _, err := fmt.Fprint(writer, prompt); err != nil {
			return "", err
		}

		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}

		value := strings.TrimSpace(line)
		if value == "" {
			value = strings.TrimSpace(defaultValue)
		}
		if value != "" || !required {
			return value, nil
		}
		if err == io.EOF {
			return "", fmt.Errorf("%s is required", label)
		}

		if _, writeErr := fmt.Fprintf(writer, "%s is required.\n", label); writeErr != nil {
			return "", writeErr
		}
	}
}

func (c CLI) enableAndStartService(ctx context.Context, spec platformSpec) error {
	switch spec.Manager {
	case serviceManagerSystemd:
		if err := c.runSystemctl(ctx, "daemon-reload"); err != nil {
			return err
		}
		if err := c.runSystemctl(ctx, "enable", "--now", spec.ServiceName); err != nil {
			return err
		}
		return nil
	case serviceManagerLaunchd:
		target := launchdTarget(spec)
		if c.isLaunchdLoaded(ctx, spec) {
			if err := c.runLaunchctl(ctx, "bootout", target); err != nil {
				return err
			}
		}
		if err := c.runLaunchctl(ctx, "bootstrap", spec.ServiceDomain, spec.ServiceUnit); err != nil {
			return err
		}
		if err := c.runLaunchctl(ctx, "enable", target); err != nil {
			return err
		}
		if err := c.runLaunchctl(ctx, "kickstart", "-k", target); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported service manager %q", spec.Manager)
	}
}

func (c CLI) runLaunchdAction(ctx context.Context, spec platformSpec, action string) error {
	target := launchdTarget(spec)
	loaded := c.isLaunchdLoaded(ctx, spec)

	switch action {
	case "start":
		if loaded {
			return c.runLaunchctl(ctx, "kickstart", "-k", target)
		}
		if err := c.runLaunchctl(ctx, "bootstrap", spec.ServiceDomain, spec.ServiceUnit); err != nil {
			return err
		}
		return c.runLaunchctl(ctx, "kickstart", "-k", target)
	case "stop":
		return c.runLaunchctl(ctx, "bootout", target)
	case "restart":
		if loaded {
			return c.runLaunchctl(ctx, "kickstart", "-k", target)
		}
		if err := c.runLaunchctl(ctx, "bootstrap", spec.ServiceDomain, spec.ServiceUnit); err != nil {
			return err
		}
		return c.runLaunchctl(ctx, "kickstart", "-k", target)
	case "status":
		return c.runLaunchctl(ctx, "print", target)
	default:
		return fmt.Errorf("unsupported service action %q", action)
	}
}

func (c CLI) isLaunchdLoaded(ctx context.Context, spec platformSpec) bool {
	cmd := exec.CommandContext(ctx, "launchctl", "print", launchdTarget(spec))
	return cmd.Run() == nil
}

func launchdTarget(spec platformSpec) string {
	return spec.ServiceDomain + "/" + spec.ServiceName
}

func (c CLI) runSystemctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.Stdout = c.stdoutOrDefault()
	cmd.Stderr = c.stderrOrDefault()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (c CLI) runLaunchctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "launchctl", args...)
	cmd.Stdout = c.stdoutOrDefault()
	cmd.Stderr = c.stderrOrDefault()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (c CLI) stdinOrDefault() io.Reader {
	if c.Stdin != nil {
		return c.Stdin
	}
	return os.Stdin
}

func (c CLI) stdoutOrDefault() io.Writer {
	if c.Stdout != nil {
		return c.Stdout
	}
	return os.Stdout
}

func (c CLI) stderrOrDefault() io.Writer {
	if c.Stderr != nil {
		return c.Stderr
	}
	return os.Stderr
}
