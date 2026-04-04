package agentctl

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/noderax/noderax-agent/internal/agent"
	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/brand"
	"github.com/noderax/noderax-agent/internal/config"
)

const (
	serviceManagerSystemd = "systemd"
	serviceManagerLaunchd = "launchd"

	linuxInstallDir                  = "/opt/noderax-agent"
	linuxBinaryPath                  = linuxInstallDir + "/noderax-agent"
	linuxSymlinkPath                 = "/usr/local/bin/noderax-agent"
	linuxPrivilegedUpdateHelperPath  = "/usr/local/libexec/noderax-agent-self-update"
	linuxRootProfileHelperPath       = "/usr/local/libexec/noderax-agent-root-profile"
	linuxPackageMutationHelperPath   = "/usr/local/libexec/noderax-agent-package-mutation"
	linuxTaskRootHelperPath          = "/usr/local/libexec/noderax-agent-task-root"
	linuxPrivilegedUpdateRequestPath = linuxServiceHome + "/update-request.json"
	linuxPackageMutationRequestPath  = linuxServiceHome + "/package-mutation-request.txt"
	linuxTaskRootRequestPath         = linuxServiceHome + "/task-root-request.txt"
	linuxConfigPath                  = "/etc/noderax-agent/config.json"
	linuxStatePath                   = "/var/lib/noderax-agent/agent_identity.json"
	linuxServiceUnit                 = "/etc/systemd/system/noderax-agent.service"
	linuxServiceName                 = "noderax-agent.service"
	linuxServiceUser                 = "noderax"
	linuxServiceHome                 = "/var/lib/noderax-agent"
	linuxBaseSudoersPath             = "/etc/sudoers.d/noderax-agent"
	linuxRootAccessSudoersPath       = "/etc/sudoers.d/noderax-agent-root-access"

	macOSInstallDir  = "/usr/local/lib/noderax-agent"
	macOSBinaryPath  = macOSInstallDir + "/noderax-agent"
	macOSSymlinkPath = "/usr/local/bin/noderax-agent"
	macOSConfigPath  = "/usr/local/etc/noderax-agent/config.json"
	macOSStatePath   = "/usr/local/var/lib/noderax-agent/agent_identity.json"
	macOSServiceUnit = "/Library/LaunchDaemons/com.noderax.agent.plist"
	macOSServiceName = "com.noderax.agent"
)

type platformSpec struct {
	Manager                    string
	InstallDir                 string
	BinaryPath                 string
	SymlinkPath                string
	PrivilegedUpdateHelperPath string
	RootProfileHelperPath      string
	PackageMutationHelperPath  string
	TaskRootHelperPath         string
	BaseSudoersPath            string
	RootAccessSudoersPath      string
	ConfigPath                 string
	StatePath                  string
	ServiceUnit                string
	ServiceName                string
	WorkingDir                 string
	RequiresRoot               bool
	ServiceDomain              string
	LogStdoutPath              string
	LogStderrPath              string
	ServiceUser                string
	ServiceGroup               string
	ServiceHome                string
}

type installOptions struct {
	APIURL         string
	BootstrapToken string
	LogLevel       string
	NonInteractive bool
}

type bootstrapOptions struct {
	APIURL         string
	BootstrapToken string
	ConfigFile     string
	StateFile      string
	LogLevel       string
	NonInteractive bool
}

type CLI struct {
	Logger    *slog.Logger
	Version   string
	Commit    string
	BuildDate string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
}

func (c CLI) Handle(ctx context.Context, args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}

	switch args[0] {
	case "install":
		brand.PrintLogo(c.stdoutOrDefault())
		return true, c.Install(ctx, args[1:])
	case "bootstrap":
		return true, c.Bootstrap(ctx, args[1:])
	case "uninstall":
		brand.PrintLogo(c.stdoutOrDefault())
		return true, c.Uninstall(ctx)
	case "start", "stop", "restart", "status":
		brand.PrintLogo(c.stdoutOrDefault())
		return true, c.ServiceAction(ctx, args[0])
	case "update":
		brand.PrintLogo(c.stdoutOrDefault())
		return true, c.Update(ctx, args[1:])
	case "config":
		brand.PrintLogo(c.stdoutOrDefault())
		return true, c.Config(ctx, args[1:])
	case "version", "--version", "-v":
		return true, c.VersionInfo(args[1:])
	default:
		return false, nil
	}
}

func (c CLI) VersionInfo(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unexpected version arguments: %s", strings.Join(args, " "))
	}

	_, _ = fmt.Fprint(
		c.stdoutOrDefault(),
		renderVersionSummary(c.Version, c.Commit, c.BuildDate),
	)
	return nil
}

func (c CLI) Install(ctx context.Context, args []string) error {
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
	options, err := parseInstallOptions(args)
	if err != nil {
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

	if options.NonInteractive {
		cfg.APIURL = firstNonEmpty(options.APIURL, cfg.APIURL)
		cfg.LogLevel = firstNonEmpty(options.LogLevel, cfg.LogLevel)
		cfg.EnrollmentToken = strings.TrimSpace(options.BootstrapToken)
		if strings.TrimSpace(cfg.APIURL) == "" {
			return fmt.Errorf("non-interactive install requires --api-url")
		}
		if strings.TrimSpace(cfg.EnrollmentToken) == "" {
			return fmt.Errorf("non-interactive install requires --bootstrap-token")
		}
	} else {
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
	}
	cfg.ConfigFile = managedConfigPath(spec)
	cfg.StateFile = spec.StatePath
	if !options.NonInteractive {
		cfg.EnrollmentToken = ""
	}
	cfg.NodeID = ""
	cfg.AgentToken = ""

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.SaveFile(cfg.ConfigFile, cfg); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	if err := ensureOwnership(spec.ServiceUser, spec.ServiceGroup, cfg.ConfigFile); err != nil {
		return err
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
	if err := writePrivilegedUpdateHelper(spec); err != nil {
		return fmt.Errorf("write privileged update helper: %w", err)
	}
	if err := writeRootProfileHelper(spec); err != nil {
		return fmt.Errorf("write root profile helper: %w", err)
	}
	if err := writePackageMutationHelper(spec); err != nil {
		return fmt.Errorf("write package mutation helper: %w", err)
	}
	if err := writeTaskRootHelper(spec); err != nil {
		return fmt.Errorf("write task root helper: %w", err)
	}
	if err := writeBaseSudoers(spec); err != nil {
		return fmt.Errorf("write base sudoers: %w", err)
	}
	if err := applyRootAccessProfile(spec, "off"); err != nil {
		return fmt.Errorf("apply default root access profile: %w", err)
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

	if options.NonInteractive {
		if err := c.bootstrapManagedInstall(ctx, spec, cfg); err != nil {
			return err
		}
	} else {
		client := api.NewClient(cfg.APIURL, cfg.RequestTimeout)
		if err := agent.RunInteractiveEnrollment(ctx, cfg, client, c.Logger, c.Version, c.stdinOrDefault(), c.stdoutOrDefault()); err != nil {
			return err
		}
	}

	if err := c.enableAndStartService(ctx, spec); err != nil {
		return err
	}

	_, _ = fmt.Fprint(c.stdoutOrDefault(), renderInstallSummary(spec, cfg, c.Version, mirroredConfigPath(cfg.ConfigFile)))
	return nil
}

func (c CLI) Bootstrap(ctx context.Context, args []string) error {
	options, err := parseBootstrapOptions(args)
	if err != nil {
		return err
	}
	if !options.NonInteractive {
		return fmt.Errorf("bootstrap currently requires --non-interactive")
	}

	cfg := config.Default()
	if strings.TrimSpace(options.ConfigFile) != "" {
		if existing, err := config.LoadFile(options.ConfigFile); err == nil {
			cfg = existing
		}
		cfg.ConfigFile = options.ConfigFile
	}

	cfg.APIURL = firstNonEmpty(options.APIURL, cfg.APIURL)
	cfg.StateFile = firstNonEmpty(options.StateFile, cfg.StateFile)
	cfg.LogLevel = firstNonEmpty(options.LogLevel, cfg.LogLevel)
	cfg.EnrollmentToken = strings.TrimSpace(options.BootstrapToken)
	cfg.NodeID = ""
	cfg.AgentToken = ""

	if strings.TrimSpace(cfg.APIURL) == "" {
		return fmt.Errorf("bootstrap requires --api-url")
	}
	if strings.TrimSpace(cfg.EnrollmentToken) == "" {
		return fmt.Errorf("bootstrap requires --bootstrap-token")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ConfigFile) != "" {
		if err := config.SaveFile(cfg.ConfigFile, cfg); err != nil {
			return fmt.Errorf("write config file: %w", err)
		}
	}

	client := api.NewClient(cfg.APIURL, cfg.RequestTimeout)
	_, err = agent.RunBootstrapEnrollment(
		ctx,
		cfg,
		client,
		c.Logger,
		c.Version,
		c.stdoutOrDefault(),
		cfg.EnrollmentToken,
	)
	return err
}

func (c CLI) Uninstall(ctx context.Context) error {
	spec, err := currentPlatformSpec()
	if err != nil {
		return err
	}
	if spec.RequiresRoot {
		if err := requireRoot(); err != nil {
			return err
		}
	}

	configPath := installedConfigPath(spec)
	statePath := installedStatePath(configPath, spec.StatePath)

	if err := c.stopAndDisableService(ctx, spec); err != nil {
		return err
	}

	removed := make([]string, 0, 8)
	missing := make([]string, 0, 8)

	unitRemoved, err := removeFileIfExists(spec.ServiceUnit)
	if err != nil {
		return err
	}
	recordRemovalResult(&removed, &missing, "service unit", spec.ServiceUnit, unitRemoved)

	switch spec.Manager {
	case serviceManagerSystemd:
		if commandExists("systemctl") {
			_ = c.runSystemctl(ctx, "daemon-reload")
			_ = c.runSystemctl(ctx, "reset-failed", spec.ServiceName)
		}
	case serviceManagerLaunchd:
		for _, logPath := range []string{spec.LogStdoutPath, spec.LogStderrPath} {
			logRemoved, err := removeFileIfExists(logPath)
			if err != nil {
				return err
			}
			recordRemovalResult(&removed, &missing, "log file", logPath, logRemoved)
		}
	}

	symlinkRemoved, err := removeFileIfExists(spec.SymlinkPath)
	if err != nil {
		return err
	}
	recordRemovalResult(&removed, &missing, "symlink", spec.SymlinkPath, symlinkRemoved)

	helperRemoved, err := removeFileIfExists(spec.PrivilegedUpdateHelperPath)
	if err != nil {
		return err
	}
	recordRemovalResult(
		&removed,
		&missing,
		"privileged update helper",
		spec.PrivilegedUpdateHelperPath,
		helperRemoved,
	)

	rootProfileHelperRemoved, err := removeFileIfExists(spec.RootProfileHelperPath)
	if err != nil {
		return err
	}
	recordRemovalResult(
		&removed,
		&missing,
		"root profile helper",
		spec.RootProfileHelperPath,
		rootProfileHelperRemoved,
	)

	packageMutationHelperRemoved, err := removeFileIfExists(spec.PackageMutationHelperPath)
	if err != nil {
		return err
	}
	recordRemovalResult(
		&removed,
		&missing,
		"package mutation helper",
		spec.PackageMutationHelperPath,
		packageMutationHelperRemoved,
	)

	taskRootHelperRemoved, err := removeFileIfExists(spec.TaskRootHelperPath)
	if err != nil {
		return err
	}
	recordRemovalResult(
		&removed,
		&missing,
		"task root helper",
		spec.TaskRootHelperPath,
		taskRootHelperRemoved,
	)

	baseSudoersRemoved, err := removeFileIfExists(spec.BaseSudoersPath)
	if err != nil {
		return err
	}
	recordRemovalResult(
		&removed,
		&missing,
		"base sudoers",
		spec.BaseSudoersPath,
		baseSudoersRemoved,
	)

	rootAccessSudoersRemoved, err := removeFileIfExists(spec.RootAccessSudoersPath)
	if err != nil {
		return err
	}
	recordRemovalResult(
		&removed,
		&missing,
		"root access sudoers",
		spec.RootAccessSudoersPath,
		rootAccessSudoersRemoved,
	)

	configRemoved, err := removeFileIfExists(configPath)
	if err != nil {
		return err
	}
	recordRemovalResult(&removed, &missing, "config file", configPath, configRemoved)

	stateRemoved, err := removeFileIfExists(statePath)
	if err != nil {
		return err
	}
	recordRemovalResult(&removed, &missing, "state file", statePath, stateRemoved)

	installRemoved, err := removeDirIfExists(spec.InstallDir)
	if err != nil {
		return err
	}
	recordRemovalResult(&removed, &missing, "install directory", spec.InstallDir, installRemoved)

	for _, dir := range []string{
		filepath.Dir(spec.ConfigPath),
		filepath.Dir(spec.StatePath),
	} {
		dirRemoved, err := removeDirIfExists(dir)
		if err != nil {
			return err
		}
		recordRemovalResult(&removed, &missing, "managed directory", dir, dirRemoved)
	}

	_, _ = fmt.Fprint(c.stdoutOrDefault(), renderUninstallSummary(spec, removed, missing))
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
	if err := ensureOwnership(spec.ServiceUser, spec.ServiceGroup, cfg.ConfigFile); err != nil {
		return err
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
			Manager:                    serviceManagerSystemd,
			InstallDir:                 linuxInstallDir,
			BinaryPath:                 linuxBinaryPath,
			SymlinkPath:                linuxSymlinkPath,
			PrivilegedUpdateHelperPath: linuxPrivilegedUpdateHelperPath,
			RootProfileHelperPath:      linuxRootProfileHelperPath,
			PackageMutationHelperPath:  linuxPackageMutationHelperPath,
			TaskRootHelperPath:         linuxTaskRootHelperPath,
			BaseSudoersPath:            linuxBaseSudoersPath,
			RootAccessSudoersPath:      linuxRootAccessSudoersPath,
			ConfigPath:                 linuxConfigPath,
			StatePath:                  linuxStatePath,
			ServiceUnit:                linuxServiceUnit,
			ServiceName:                linuxServiceName,
			WorkingDir:                 linuxInstallDir,
			RequiresRoot:               true,
			ServiceDomain:              "system",
			ServiceUser:                linuxServiceUser,
			ServiceGroup:               linuxServiceUser,
			ServiceHome:                linuxServiceHome,
		}, nil
	case "darwin":
		return platformSpec{
			Manager:                    serviceManagerLaunchd,
			InstallDir:                 macOSInstallDir,
			BinaryPath:                 macOSBinaryPath,
			SymlinkPath:                macOSSymlinkPath,
			PrivilegedUpdateHelperPath: "",
			RootProfileHelperPath:      "",
			PackageMutationHelperPath:  "",
			TaskRootHelperPath:         "",
			BaseSudoersPath:            "",
			RootAccessSudoersPath:      "",
			ConfigPath:                 macOSConfigPath,
			StatePath:                  macOSStatePath,
			ServiceUnit:                macOSServiceUnit,
			ServiceName:                macOSServiceName,
			WorkingDir:                 macOSInstallDir,
			RequiresRoot:               true,
			ServiceDomain:              "system",
			LogStdoutPath:              "/var/log/noderax-agent.log",
			LogStderrPath:              "/var/log/noderax-agent.error.log",
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

func parseInstallOptions(args []string) (installOptions, error) {
	var options installOptions

	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&options.APIURL, "api-url", "", "")
	fs.StringVar(&options.BootstrapToken, "bootstrap-token", "", "")
	fs.StringVar(&options.LogLevel, "log-level", "", "")
	fs.BoolVar(&options.NonInteractive, "non-interactive", false, "")

	if err := fs.Parse(args); err != nil {
		return installOptions{}, fmt.Errorf("parse install flags: %w", err)
	}
	if len(fs.Args()) > 0 {
		return installOptions{}, fmt.Errorf("unexpected install arguments: %s", strings.Join(fs.Args(), " "))
	}

	return options, nil
}

func parseBootstrapOptions(args []string) (bootstrapOptions, error) {
	var options bootstrapOptions

	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&options.APIURL, "api-url", "", "")
	fs.StringVar(&options.BootstrapToken, "bootstrap-token", "", "")
	fs.StringVar(&options.ConfigFile, "config-file", "", "")
	fs.StringVar(&options.StateFile, "state-file", "", "")
	fs.StringVar(&options.LogLevel, "log-level", "", "")
	fs.BoolVar(&options.NonInteractive, "non-interactive", false, "")

	if err := fs.Parse(args); err != nil {
		return bootstrapOptions{}, fmt.Errorf("parse bootstrap flags: %w", err)
	}
	if len(fs.Args()) > 0 {
		return bootstrapOptions{}, fmt.Errorf("unexpected bootstrap arguments: %s", strings.Join(fs.Args(), " "))
	}

	return options, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
	case "metrics_interval":
		return setDuration(&cfg.MetricsInterval, value)
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
	userSection := ""
	if strings.TrimSpace(spec.ServiceUser) != "" {
		userSection += fmt.Sprintf("User=%s\n", spec.ServiceUser)
	}
	if strings.TrimSpace(spec.ServiceGroup) != "" {
		userSection += fmt.Sprintf("Group=%s\n", spec.ServiceGroup)
	}
	if strings.TrimSpace(spec.ServiceHome) != "" {
		userSection += fmt.Sprintf("Environment=HOME=%s\n", spec.ServiceHome)
	}

	return fmt.Sprintf(`[Unit]
Description=Noderax Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
%sEnvironment=NODERAX_CONFIG_FILE=%s
ExecStart=%s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, spec.WorkingDir, userSection, configPath, spec.BinaryPath)
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

func renderInstallSummary(spec platformSpec, cfg config.Config, version, localConfigPath string) string {
	var builder strings.Builder

	builder.WriteString("\n")
	builder.WriteString("========================================\n")
	builder.WriteString(" Noderax Agent Ready\n")
	builder.WriteString("========================================\n")
	fmt.Fprintf(&builder, "Status       : running in the background\n")
	fmt.Fprintf(&builder, "Platform     : %s (%s)\n", runtime.GOOS, spec.Manager)
	if strings.TrimSpace(version) != "" {
		fmt.Fprintf(&builder, "Version      : %s\n", strings.TrimSpace(version))
	}
	fmt.Fprintf(&builder, "Service      : %s\n", spec.ServiceName)
	fmt.Fprintf(&builder, "API URL      : %s\n", cfg.APIURL)
	fmt.Fprintf(&builder, "Binary       : %s\n", spec.BinaryPath)
	fmt.Fprintf(&builder, "Config       : %s\n", cfg.ConfigFile)
	if strings.TrimSpace(localConfigPath) != "" {
		fmt.Fprintf(&builder, "Local config : %s\n", localConfigPath)
	}
	fmt.Fprintf(&builder, "State        : %s\n", cfg.StateFile)
	fmt.Fprintf(&builder, "Logs         : %s\n", logHintForSummary(spec))

	builder.WriteString("\n")
	builder.WriteString("Commands\n")
	fmt.Fprintf(&builder, "  start   %s\n", serviceCommandForSummary("start"))
	fmt.Fprintf(&builder, "  stop    %s\n", serviceCommandForSummary("stop"))
	fmt.Fprintf(&builder, "  restart %s\n", serviceCommandForSummary("restart"))
	fmt.Fprintf(&builder, "  status  %s\n", serviceCommandForSummary("status"))
	fmt.Fprintf(&builder, "  version %s\n", versionCommandForSummary())
	fmt.Fprintf(&builder, "  config-show %s\n", configShowCommandForSummary())
	fmt.Fprintf(&builder, "  config-set  %s\n", configSetCommandForSummary())
	fmt.Fprintf(&builder, "  update      %s\n", agentUpdateCommandForSummary())
	fmt.Fprintf(&builder, "  remove  %s\n", uninstallCommandForSummary())

	builder.WriteString("\n")
	builder.WriteString("Notes\n")
	builder.WriteString("  The service was started automatically during install.\n")
	builder.WriteString("  Use the commands above whenever you want to manage it again.\n")

	return builder.String()
}

func renderVersionSummary(version, commit, buildDate string) string {
	var builder strings.Builder

	builder.WriteString("Noderax Agent\n")
	fmt.Fprintf(&builder, "Version    : %s\n", firstNonEmpty(strings.TrimSpace(version), "unknown"))
	fmt.Fprintf(&builder, "Commit     : %s\n", firstNonEmpty(strings.TrimSpace(commit), "unknown"))
	fmt.Fprintf(&builder, "Build date : %s\n", firstNonEmpty(strings.TrimSpace(buildDate), "unknown"))

	return builder.String()
}

func renderUninstallSummary(spec platformSpec, removed, missing []string) string {
	var builder strings.Builder

	builder.WriteString("\n")
	builder.WriteString("========================================\n")
	builder.WriteString(" Noderax Agent Removed\n")
	builder.WriteString("========================================\n")
	fmt.Fprintf(&builder, "Platform     : %s (%s)\n", runtime.GOOS, spec.Manager)
	fmt.Fprintf(&builder, "Service      : %s\n", spec.ServiceName)

	builder.WriteString("\n")
	builder.WriteString("Removed\n")
	for _, item := range removed {
		fmt.Fprintf(&builder, "  %s\n", item)
	}
	if len(removed) == 0 {
		builder.WriteString("  No installed artifacts were found.\n")
	}

	if len(missing) > 0 {
		builder.WriteString("\n")
		builder.WriteString("Already absent\n")
		for _, item := range missing {
			fmt.Fprintf(&builder, "  %s\n", item)
		}
	}

	builder.WriteString("\n")
	builder.WriteString("Next\n")
	builder.WriteString("  Reinstall anytime with sudo ./scripts/install.sh\n")

	return builder.String()
}

func mirroredConfigPath(managedConfigPath string) string {
	mirrorPath := strings.TrimSpace(os.Getenv("NODERAX_CONFIG_MIRROR_FILE"))
	if mirrorPath == "" {
		return ""
	}

	mirrorPath = filepath.Clean(mirrorPath)
	if mirrorPath == filepath.Clean(managedConfigPath) {
		return ""
	}

	return mirrorPath
}

func serviceCommandForSummary(action string) string {
	return "sudo noderax-agent " + strings.TrimSpace(action)
}

func configShowCommandForSummary() string {
	return "sudo noderax-agent config show"
}

func versionCommandForSummary() string {
	return "noderax-agent version"
}

func configSetCommandForSummary() string {
	return "sudo noderax-agent config set api_url https://api.example.com"
}

func uninstallCommandForSummary() string {
	return "sudo noderax-agent uninstall"
}

func agentUpdateCommandForSummary() string {
	return "sudo noderax-agent update --target-version 1.2.3 --target-id <rollout-target-id>"
}

func logHintForSummary(spec platformSpec) string {
	switch spec.Manager {
	case serviceManagerSystemd:
		return "sudo journalctl -u " + spec.ServiceName + " -f"
	case serviceManagerLaunchd:
		if strings.TrimSpace(spec.LogStdoutPath) != "" {
			return "sudo tail -f " + spec.LogStdoutPath
		}
		return "sudo launchctl print " + launchdTarget(spec)
	default:
		return "noderax-agent status"
	}
}

func installedConfigPath(spec platformSpec) string {
	if path := configPathFromServiceDefinition(spec.ServiceUnit); path != "" {
		return path
	}
	if value := strings.TrimSpace(os.Getenv("NODERAX_CONFIG_FILE")); value != "" {
		return filepath.Clean(value)
	}
	return spec.ConfigPath
}

func installedStatePath(configPath, fallback string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return fallback
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fallback
	}

	var raw struct {
		StateFile string `json:"state_file"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fallback
	}
	if strings.TrimSpace(raw.StateFile) == "" {
		return fallback
	}

	return filepath.Clean(raw.StateFile)
}

func configPathFromServiceDefinition(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	content := string(data)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "NODERAX_CONFIG_FILE=") {
			continue
		}
		parts := strings.SplitN(line, "NODERAX_CONFIG_FILE=", 2)
		if len(parts) != 2 {
			continue
		}
		return filepath.Clean(strings.TrimSpace(parts[1]))
	}

	keyIndex := strings.Index(content, "<key>NODERAX_CONFIG_FILE</key>")
	if keyIndex == -1 {
		return ""
	}

	fragment := content[keyIndex:]
	openTag := "<string>"
	closeTag := "</string>"
	start := strings.Index(fragment, openTag)
	end := strings.Index(fragment, closeTag)
	if start == -1 || end == -1 || end <= start+len(openTag) {
		return ""
	}

	return filepath.Clean(strings.TrimSpace(fragment[start+len(openTag) : end]))
}

func recordRemovalResult(removed, missing *[]string, kind, path string, didRemove bool) {
	if strings.TrimSpace(path) == "" {
		return
	}

	entry := kind + ": " + path
	if didRemove {
		*removed = append(*removed, entry)
		return
	}

	*missing = append(*missing, entry)
}

func removeFileIfExists(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, nil
	}

	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}

	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("remove %s: %w", path, err)
	}

	return true, nil
}

func removeDirIfExists(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("expected directory at %s", path)
	}

	if err := os.RemoveAll(path); err != nil {
		return false, fmt.Errorf("remove directory %s: %w", path, err)
	}

	return true, nil
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

func writePrivilegedUpdateHelper(spec platformSpec) error {
	path := strings.TrimSpace(spec.PrivilegedUpdateHelperPath)
	if path == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create helper directory for %s: %w", path, err)
	}

	content := renderPrivilegedUpdateHelper(spec)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return fmt.Errorf("write helper %s: %w", path, err)
	}

	return nil
}

func writeRootProfileHelper(spec platformSpec) error {
	path := strings.TrimSpace(spec.RootProfileHelperPath)
	if path == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create helper directory for %s: %w", path, err)
	}

	content := renderRootProfileHelper(spec)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return fmt.Errorf("write helper %s: %w", path, err)
	}

	return nil
}

func writePackageMutationHelper(spec platformSpec) error {
	path := strings.TrimSpace(spec.PackageMutationHelperPath)
	if path == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create helper directory for %s: %w", path, err)
	}

	content := renderPackageMutationHelper(spec)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return fmt.Errorf("write helper %s: %w", path, err)
	}

	return nil
}

func writeTaskRootHelper(spec platformSpec) error {
	path := strings.TrimSpace(spec.TaskRootHelperPath)
	if path == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create helper directory for %s: %w", path, err)
	}

	content := renderTaskRootHelper(spec)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return fmt.Errorf("write helper %s: %w", path, err)
	}

	return nil
}

func writeBaseSudoers(spec platformSpec) error {
	path := strings.TrimSpace(spec.BaseSudoersPath)
	if path == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create sudoers directory for %s: %w", path, err)
	}

	if err := os.WriteFile(path, []byte(renderBaseSudoers(spec)), 0o440); err != nil {
		return fmt.Errorf("write sudoers file %s: %w", path, err)
	}

	if commandExists("visudo") {
		cmd := exec.Command("visudo", "-cf", path)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("validate sudoers file %s: %w: %s", path, err, strings.TrimSpace(string(output)))
		}
	}

	return nil
}

func applyRootAccessProfile(spec platformSpec, profile string) error {
	helperPath := strings.TrimSpace(spec.RootProfileHelperPath)
	if helperPath == "" {
		return nil
	}

	cmd := exec.Command(helperPath, "apply", strings.TrimSpace(profile))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run root profile helper: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func renderPrivilegedUpdateHelper(spec platformSpec) string {
	return fmt.Sprintf(`#!/bin/sh
set -eu

if [ "$#" -ne 0 ]; then
  echo "usage: %s" >&2
  exit 64
fi

exec %q update --request-file %q
`, spec.PrivilegedUpdateHelperPath, spec.BinaryPath, linuxPrivilegedUpdateRequestPath)
}

func renderPackageMutationHelper(spec platformSpec) string {
	return fmt.Sprintf(`#!/bin/sh
set -eu

HELPER_PATH=%q
REQUEST_FILE=%q

if [ "$#" -ne 0 ]; then
	echo "usage: ${HELPER_PATH}" >&2
	exit 64
fi

if [ ! -f "${REQUEST_FILE}" ]; then
	echo "package mutation request file is missing" >&2
	exit 1
fi

OPERATION=""
set --
LINE_NO=0
while IFS= read -r line || [ -n "${line}" ]; do
	LINE_NO=$((LINE_NO + 1))
	if [ "${LINE_NO}" -eq 1 ]; then
		OPERATION="${line}"
		continue
	fi

	pkg="$(printf '%%s' "${line}" | tr -d '[:space:]')"
	if [ -z "${pkg}" ]; then
		continue
	fi

	if ! printf '%%s\n' "${pkg}" | grep -Eq '^[a-z0-9][a-z0-9.+:-]*$'; then
		echo "invalid package name in request: ${pkg}" >&2
		rm -f "${REQUEST_FILE}"
		exit 64
	fi

	set -- "$@" "${pkg}"
done < "${REQUEST_FILE}"

rm -f "${REQUEST_FILE}"

APT_GET_PATH="$(command -v apt-get || true)"
if [ -z "${APT_GET_PATH}" ]; then
	echo "apt-get is required for package mutation helper" >&2
	exit 1
fi

case "${OPERATION}" in
	update)
		exec "${APT_GET_PATH}" update
		;;
	install|remove|purge)
		if [ "$#" -eq 0 ]; then
			echo "package mutation request must include package names for ${OPERATION}" >&2
			exit 64
		fi
		exec "${APT_GET_PATH}" "${OPERATION}" -y -- "$@"
		;;
	*)
		echo "unsupported package mutation operation: ${OPERATION}" >&2
		exit 64
		;;
esac
`, spec.PackageMutationHelperPath, linuxPackageMutationRequestPath)
}

func renderTaskRootHelper(spec platformSpec) string {
	return fmt.Sprintf(`#!/bin/sh
set -eu

HELPER_PATH=%q
REQUEST_FILE=%q

if [ "$#" -ne 0 ]; then
	echo "usage: ${HELPER_PATH}" >&2
	exit 64
fi

if [ ! -f "${REQUEST_FILE}" ]; then
	echo "task root request file is missing" >&2
	exit 1
fi

COMMAND="$(cat "${REQUEST_FILE}")"
rm -f "${REQUEST_FILE}"

if [ -z "$(printf '%%s' "${COMMAND}" | tr -d '[:space:]')" ]; then
	echo "task root request is empty" >&2
	exit 64
fi

exec /bin/sh -lc "${COMMAND}"
`, spec.TaskRootHelperPath, linuxTaskRootRequestPath)
}

func renderBaseSudoers(spec platformSpec) string {
	rootProfileHelperPath := spec.RootProfileHelperPath
	rootProfileCommands := strings.Join([]string{
		rootProfileHelperPath + " apply off",
		rootProfileHelperPath + " apply operational",
		rootProfileHelperPath + " apply task",
		rootProfileHelperPath + " apply terminal",
		rootProfileHelperPath + " apply all",
	}, ", ")
	explicitAllowedCommands := strings.Join([]string{
		spec.PrivilegedUpdateHelperPath,
		rootProfileHelperPath + " apply off",
		rootProfileHelperPath + " apply operational",
		rootProfileHelperPath + " apply task",
		rootProfileHelperPath + " apply terminal",
		rootProfileHelperPath + " apply all",
	}, ", ")

	return fmt.Sprintf(`# Managed by the Noderax agent installer.
Cmnd_Alias NODERAX_AGENT_SELF_UPDATE = %s
Cmnd_Alias NODERAX_AGENT_ROOT_PROFILE = %s
%s ALL=(root) NOPASSWD: %s
`, spec.PrivilegedUpdateHelperPath, rootProfileCommands, spec.ServiceUser, explicitAllowedCommands)
}

func renderRootProfileHelper(spec platformSpec) string {
	return fmt.Sprintf(`#!/bin/sh
set -eu

HELPER_PATH=%q
SERVICE_USER=%q
SERVICE_NAME=%q
SUDOERS_FILE=%q
PACKAGE_MUTATION_HELPER=%q
TASK_ROOT_HELPER=%q

if [ "$#" -ne 2 ] || [ "$1" != "apply" ]; then
	echo "usage: ${HELPER_PATH} apply <off|operational|task|terminal|operational_task|operational_terminal|task_terminal|all>" >&2
  exit 64
fi

PROFILE="$2"
mkdir -p "$(dirname "${SUDOERS_FILE}")"

if [ "${PROFILE}" = "off" ]; then
  rm -f "${SUDOERS_FILE}"
  exit 0
fi

TMP_FILE="$(mktemp "${SUDOERS_FILE}.XXXXXX")"
cleanup() {
  rm -f "${TMP_FILE}"
}
trap cleanup EXIT INT TERM

append_line() {
  printf '%%s\n' "$1" >> "${TMP_FILE}"
}

append_alias() {
  if [ -z "${ALIAS_LIST}" ]; then
    ALIAS_LIST="$1"
  else
    ALIAS_LIST="${ALIAS_LIST}, $1"
  fi
}

append_line "# Managed by the Noderax agent root profile helper."
ALIAS_LIST=""

append_operational_profile() {
  APT_GET_PATH="$(command -v apt-get || true)"
  SYSTEMCTL_PATH="$(command -v systemctl || true)"
  REBOOT_PATH="$(command -v reboot || true)"

  if [ -n "${APT_GET_PATH}" ]; then
		append_line "Cmnd_Alias NODERAX_AGENT_PACKAGE_MUTATIONS = ${PACKAGE_MUTATION_HELPER}"
    append_alias "NODERAX_AGENT_PACKAGE_MUTATIONS"
  fi

  if [ -n "${SYSTEMCTL_PATH}" ]; then
    append_line "Cmnd_Alias NODERAX_AGENT_SERVICE_CONTROL = ${SYSTEMCTL_PATH} restart ${SERVICE_NAME}, ${SYSTEMCTL_PATH} restart ${SERVICE_NAME%%.service}"
    append_alias "NODERAX_AGENT_SERVICE_CONTROL"
  fi

  if [ -n "${REBOOT_PATH}" ]; then
    append_line "Cmnd_Alias NODERAX_AGENT_REBOOT = ${REBOOT_PATH}"
    append_alias "NODERAX_AGENT_REBOOT"
  fi
}

append_task_profile() {
	append_line "Cmnd_Alias NODERAX_AGENT_TASK_ROOT = ${TASK_ROOT_HELPER}"
  append_alias "NODERAX_AGENT_TASK_ROOT"
}

append_terminal_profile() {
  TERMINAL_COMMANDS=""
  for shell_path in /bin/bash /bin/zsh /bin/sh; do
    if [ ! -x "${shell_path}" ]; then
      continue
    fi

    if [ -n "${TERMINAL_COMMANDS}" ]; then
      TERMINAL_COMMANDS="${TERMINAL_COMMANDS}, "
    fi
    TERMINAL_COMMANDS="${TERMINAL_COMMANDS}${shell_path} -i"
  done

  if [ -z "${TERMINAL_COMMANDS}" ]; then
    TERMINAL_COMMANDS="/bin/sh -i"
  fi

  append_line "Cmnd_Alias NODERAX_AGENT_TERMINAL_ROOT = ${TERMINAL_COMMANDS}"
  append_alias "NODERAX_AGENT_TERMINAL_ROOT"
}

case "${PROFILE}" in
  operational)
    append_operational_profile
    ;;
  task)
    append_task_profile
    ;;
  terminal)
    append_terminal_profile
    ;;
	operational_task)
		append_operational_profile
		append_task_profile
		;;
	operational_terminal)
		append_operational_profile
		append_terminal_profile
		;;
	task_terminal)
		append_task_profile
		append_terminal_profile
		;;
  all)
    append_operational_profile
    append_task_profile
    append_terminal_profile
    ;;
  *)
    echo "unsupported root profile: ${PROFILE}" >&2
    exit 64
    ;;
esac

if [ -z "${ALIAS_LIST}" ]; then
  echo "root profile ${PROFILE} did not produce any sudo rules" >&2
  exit 1
fi

append_line "${SERVICE_USER} ALL=(root) NOPASSWD: ${ALIAS_LIST}"
chmod 0440 "${TMP_FILE}"

if command -v visudo >/dev/null 2>&1; then
  visudo -cf "${TMP_FILE}" >/dev/null
fi

install -o root -g root -m 0440 "${TMP_FILE}" "${SUDOERS_FILE}"
`, spec.RootProfileHelperPath, spec.ServiceUser, spec.ServiceName, spec.RootAccessSudoersPath, spec.PackageMutationHelperPath, spec.TaskRootHelperPath)
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

func ensureOwnership(ownerName, groupName, path string) error {
	if os.Geteuid() != 0 || strings.TrimSpace(ownerName) == "" || strings.TrimSpace(path) == "" {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}

	owner, err := user.Lookup(ownerName)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", ownerName, err)
	}
	group, err := user.LookupGroup(firstNonEmpty(groupName, ownerName))
	if err != nil {
		return fmt.Errorf("lookup group %s: %w", firstNonEmpty(groupName, ownerName), err)
	}

	uid, err := strconv.Atoi(owner.Uid)
	if err != nil {
		return fmt.Errorf("parse uid for %s: %w", ownerName, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parse gid for %s: %w", groupName, err)
	}

	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	if info.IsDir() {
		return nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
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

func (c CLI) bootstrapManagedInstall(ctx context.Context, spec platformSpec, cfg config.Config) error {
	if strings.TrimSpace(spec.ServiceUser) == "" {
		client := api.NewClient(cfg.APIURL, cfg.RequestTimeout)
		_, err := agent.RunBootstrapEnrollment(
			ctx,
			cfg,
			client,
			c.Logger,
			c.Version,
			c.stdoutOrDefault(),
			cfg.EnrollmentToken,
		)
		return err
	}

	if err := ensureOwnership(spec.ServiceUser, spec.ServiceGroup, cfg.ConfigFile); err != nil {
		return err
	}

	args := []string{
		"-u",
		spec.ServiceUser,
		"-H",
		spec.BinaryPath,
		"bootstrap",
		"--non-interactive",
		"--api-url",
		cfg.APIURL,
		"--bootstrap-token",
		cfg.EnrollmentToken,
		"--config-file",
		cfg.ConfigFile,
		"--state-file",
		cfg.StateFile,
		"--log-level",
		cfg.LogLevel,
	}

	cmd := exec.CommandContext(ctx, "sudo", args...)
	cmd.Stdout = c.stdoutOrDefault()
	cmd.Stderr = c.stderrOrDefault()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bootstrap managed install: %w", err)
	}

	return nil
}

func (c CLI) stopAndDisableService(ctx context.Context, spec platformSpec) error {
	switch spec.Manager {
	case serviceManagerSystemd:
		if !commandExists("systemctl") {
			return nil
		}
		if c.isSystemdActive(ctx, spec) {
			if err := c.runSystemctl(ctx, "stop", spec.ServiceName); err != nil {
				return err
			}
		}
		if c.isSystemdEnabled(ctx, spec) {
			if err := c.runSystemctl(ctx, "disable", spec.ServiceName); err != nil {
				return err
			}
		}
		return nil
	case serviceManagerLaunchd:
		if !commandExists("launchctl") {
			return nil
		}
		if c.isLaunchdLoaded(ctx, spec) {
			if err := c.runLaunchctl(ctx, "bootout", launchdTarget(spec)); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported service manager %q", spec.Manager)
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

func (c CLI) isSystemdActive(ctx context.Context, spec platformSpec) bool {
	cmd := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", spec.ServiceName)
	return cmd.Run() == nil
}

func (c CLI) isSystemdEnabled(ctx context.Context, spec platformSpec) bool {
	cmd := exec.CommandContext(ctx, "systemctl", "is-enabled", "--quiet", spec.ServiceName)
	return cmd.Run() == nil
}

func launchdTarget(spec platformSpec) string {
	return spec.ServiceDomain + "/" + spec.ServiceName
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
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
