package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultHeartbeatInterval = 30 * time.Second
	defaultMetricsInterval   = 60 * time.Second
	defaultTaskPollInterval  = 15 * time.Second
	defaultRequestTimeout    = 10 * time.Second
	defaultTaskTimeout       = 10 * time.Minute
	defaultShutdownTimeout   = 20 * time.Second
	defaultStateFile         = "./data/agent_identity.json"
)

type Config struct {
	APIURL            string
	EnrollmentToken   string
	NodeID            string
	AgentToken        string
	HeartbeatInterval time.Duration
	MetricsInterval   time.Duration
	TaskPollInterval  time.Duration
	RequestTimeout    time.Duration
	TaskTimeout       time.Duration
	ShutdownTimeout   time.Duration
	StateFile         string
	ConfigFile        string
	LogLevel          string
}

type fileConfig struct {
	APIURL            string `json:"api_url"`
	EnrollmentToken   string `json:"enrollment_token"`
	NodeID            string `json:"node_id"`
	AgentToken        string `json:"agent_token"`
	HeartbeatInterval string `json:"heartbeat_interval"`
	MetricsInterval   string `json:"metrics_interval"`
	TaskPollInterval  string `json:"task_poll_interval"`
	RequestTimeout    string `json:"request_timeout"`
	TaskTimeout       string `json:"task_timeout"`
	ShutdownTimeout   string `json:"shutdown_timeout"`
	StateFile         string `json:"state_file"`
	LogLevel          string `json:"log_level"`
}

func Load() (Config, error) {
	cfg := Config{
		HeartbeatInterval: defaultHeartbeatInterval,
		MetricsInterval:   defaultMetricsInterval,
		TaskPollInterval:  defaultTaskPollInterval,
		RequestTimeout:    defaultRequestTimeout,
		TaskTimeout:       defaultTaskTimeout,
		ShutdownTimeout:   defaultShutdownTimeout,
		StateFile:         defaultStateFile,
		LogLevel:          "info",
	}

	if configFile := detectConfigFile(); configFile != "" {
		if err := mergeConfigFile(&cfg, configFile); err != nil {
			return Config{}, err
		}
		cfg.ConfigFile = configFile
	}

	if err := mergeEnv(&cfg); err != nil {
		return Config{}, err
	}

	cfg.normalize()

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func SaveFile(path string, cfg Config) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("config path must not be empty")
	}

	raw := fileConfig{
		APIURL:            cfg.APIURL,
		EnrollmentToken:   cfg.EnrollmentToken,
		NodeID:            cfg.NodeID,
		AgentToken:        cfg.AgentToken,
		HeartbeatInterval: cfg.HeartbeatInterval.String(),
		MetricsInterval:   cfg.MetricsInterval.String(),
		TaskPollInterval:  cfg.TaskPollInterval.String(),
		RequestTimeout:    cfg.RequestTimeout.String(),
		TaskTimeout:       cfg.TaskTimeout.String(),
		ShutdownTimeout:   cfg.ShutdownTimeout.String(),
		StateFile:         cfg.StateFile,
		LogLevel:          cfg.LogLevel,
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory for %s: %w", path, err)
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config file %s: %w", path, err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config file %s: %w", path, err)
	}

	return nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.APIURL) == "" {
		return fmt.Errorf("API_URL is required; set API_URL or provide NODERAX_CONFIG_FILE, ./config.local.json, ./config.json, ./config/config.json, or /etc/noderax-agent/config.json")
	}
	parsedURL, err := url.Parse(c.APIURL)
	if err != nil {
		return fmt.Errorf("API_URL is invalid: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("API_URL must use http or https, got %q", parsedURL.Scheme)
	}
	if parsedURL.Host == "" {
		return fmt.Errorf("API_URL must include a host, got %q", c.APIURL)
	}
	if c.HeartbeatInterval <= 0 {
		return fmt.Errorf("HEARTBEAT_INTERVAL must be greater than zero")
	}
	if c.MetricsInterval <= 0 {
		return fmt.Errorf("METRICS_INTERVAL must be greater than zero")
	}
	if c.TaskPollInterval <= 0 {
		return fmt.Errorf("TASK_POLL_INTERVAL must be greater than zero")
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("REQUEST_TIMEOUT must be greater than zero")
	}
	if c.TaskTimeout <= 0 {
		return fmt.Errorf("TASK_TIMEOUT must be greater than zero")
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("SHUTDOWN_TIMEOUT must be greater than zero")
	}
	if strings.TrimSpace(c.StateFile) == "" {
		return fmt.Errorf("STATE_FILE must not be empty")
	}
	return nil
}

func (c *Config) normalize() {
	c.APIURL = normalizeAPIURL(c.APIURL)
}

func detectConfigFile() string {
	if value := strings.TrimSpace(os.Getenv("NODERAX_CONFIG_FILE")); value != "" {
		return value
	}

	candidates := []string{
		"./config.local.json",
		"./config.json",
		"./config/config.json",
		"/etc/noderax-agent/config.json",
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return ""
}

func mergeConfigFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file %s: %w", path, err)
	}

	var raw fileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode config file %s: %w", path, err)
	}

	if raw.APIURL != "" {
		cfg.APIURL = strings.TrimSpace(raw.APIURL)
	}
	if raw.EnrollmentToken != "" {
		cfg.EnrollmentToken = raw.EnrollmentToken
	}
	if raw.NodeID != "" {
		cfg.NodeID = raw.NodeID
	}
	if raw.AgentToken != "" {
		cfg.AgentToken = raw.AgentToken
	}
	if raw.StateFile != "" {
		cfg.StateFile = raw.StateFile
	}
	if raw.LogLevel != "" {
		cfg.LogLevel = raw.LogLevel
	}

	if err := applyDuration(&cfg.HeartbeatInterval, "heartbeat_interval", raw.HeartbeatInterval); err != nil {
		return err
	}
	if err := applyDuration(&cfg.MetricsInterval, "metrics_interval", raw.MetricsInterval); err != nil {
		return err
	}
	if err := applyDuration(&cfg.TaskPollInterval, "task_poll_interval", raw.TaskPollInterval); err != nil {
		return err
	}
	if err := applyDuration(&cfg.RequestTimeout, "request_timeout", raw.RequestTimeout); err != nil {
		return err
	}
	if err := applyDuration(&cfg.TaskTimeout, "task_timeout", raw.TaskTimeout); err != nil {
		return err
	}
	if err := applyDuration(&cfg.ShutdownTimeout, "shutdown_timeout", raw.ShutdownTimeout); err != nil {
		return err
	}

	return nil
}

func mergeEnv(cfg *Config) error {
	overrideString(&cfg.APIURL, "API_URL")
	overrideString(&cfg.EnrollmentToken, "ENROLLMENT_TOKEN")
	overrideString(&cfg.NodeID, "NODE_ID")
	overrideString(&cfg.AgentToken, "AGENT_TOKEN")
	overrideString(&cfg.StateFile, "STATE_FILE")
	overrideString(&cfg.LogLevel, "LOG_LEVEL")

	if err := overrideDuration(&cfg.HeartbeatInterval, "HEARTBEAT_INTERVAL"); err != nil {
		return err
	}
	if err := overrideDuration(&cfg.MetricsInterval, "METRICS_INTERVAL"); err != nil {
		return err
	}
	if err := overrideDuration(&cfg.TaskPollInterval, "TASK_POLL_INTERVAL"); err != nil {
		return err
	}
	if err := overrideDuration(&cfg.RequestTimeout, "REQUEST_TIMEOUT"); err != nil {
		return err
	}
	if err := overrideDuration(&cfg.TaskTimeout, "TASK_TIMEOUT"); err != nil {
		return err
	}
	if err := overrideDuration(&cfg.ShutdownTimeout, "SHUTDOWN_TIMEOUT"); err != nil {
		return err
	}

	if cfg.StateFile != "" {
		cfg.StateFile = filepath.Clean(cfg.StateFile)
	}

	return nil
}

func overrideString(target *string, key string) {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		*target = value
	}
}

func overrideDuration(target *time.Duration, key string) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	return applyDuration(target, key, value)
}

func applyDuration(target *time.Duration, name, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}

	*target = duration
	return nil
}

func normalizeAPIURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	if strings.Contains(value, "://") {
		return strings.TrimRight(value, "/")
	}

	return strings.TrimRight("http://"+value, "/")
}
