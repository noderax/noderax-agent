package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHeartbeatInterval = 30 * time.Second
	defaultMetricsInterval   = 2 * time.Second
	defaultTaskPollInterval  = 15 * time.Second
	defaultRequestTimeout    = 10 * time.Second
	defaultTaskTimeout       = 10 * time.Minute
	defaultShutdownTimeout   = 20 * time.Second
	defaultRealtimePing      = 2 * time.Second
	defaultRealtimeQueueSize = 1024
	defaultRealtimeJitter    = 0.2
	defaultRealtimeNamespace = "/agent-realtime"
	defaultRealtimePath      = "/socket.io/"
	defaultStateFile         = "./data/agent_identity.json"
	configMirrorEnv          = "NODERAX_CONFIG_MIRROR_FILE"
)

type Config struct {
	APIURL                string
	APITLSCAFile          string
	EnrollmentToken       string
	NodeID                string
	AgentToken            string
	HeartbeatInterval     time.Duration
	MetricsInterval       time.Duration
	TaskPollInterval      time.Duration
	RequestTimeout        time.Duration
	TaskTimeout           time.Duration
	ShutdownTimeout       time.Duration
	RealtimeEnabled       bool
	RealtimePingInterval  time.Duration
	RealtimeQueueSize     int
	RealtimeBackoffJitter float64
	RealtimeNamespace     string
	RealtimePath          string
	StateFile             string
	ConfigFile            string
	LogLevel              string
}

type fileConfig struct {
	APIURL                string   `json:"api_url"`
	APITLSCAFile          string   `json:"api_tls_ca_file,omitempty"`
	EnrollmentToken       string   `json:"enrollment_token"`
	NodeID                string   `json:"node_id"`
	AgentToken            string   `json:"agent_token"`
	HeartbeatInterval     string   `json:"heartbeat_interval"`
	MetricsInterval       string   `json:"metrics_interval"`
	TaskPollInterval      string   `json:"task_poll_interval"`
	RequestTimeout        string   `json:"request_timeout"`
	TaskTimeout           string   `json:"task_timeout"`
	ShutdownTimeout       string   `json:"shutdown_timeout"`
	RealtimeEnabled       *bool    `json:"realtime_enabled,omitempty"`
	RealtimePingInterval  string   `json:"realtime_ping_interval,omitempty"`
	RealtimeQueueSize     *int     `json:"realtime_queue_size,omitempty"`
	RealtimeBackoffJitter *float64 `json:"realtime_backoff_jitter,omitempty"`
	RealtimeNamespace     string   `json:"realtime_namespace,omitempty"`
	RealtimePath          string   `json:"realtime_path,omitempty"`
	StateFile             string   `json:"state_file"`
	LogLevel              string   `json:"log_level"`
}

func Default() Config {
	return Config{
		HeartbeatInterval:     defaultHeartbeatInterval,
		MetricsInterval:       defaultMetricsInterval,
		TaskPollInterval:      defaultTaskPollInterval,
		RequestTimeout:        defaultRequestTimeout,
		TaskTimeout:           defaultTaskTimeout,
		ShutdownTimeout:       defaultShutdownTimeout,
		RealtimeEnabled:       true,
		RealtimePingInterval:  defaultRealtimePing,
		RealtimeQueueSize:     defaultRealtimeQueueSize,
		RealtimeBackoffJitter: defaultRealtimeJitter,
		RealtimeNamespace:     defaultRealtimeNamespace,
		RealtimePath:          defaultRealtimePath,
		StateFile:             defaultStateFile,
		LogLevel:              "info",
	}
}

func Load() (Config, error) {
	cfg := Default()

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

func LoadFile(path string) (Config, error) {
	cfg := Default()

	if err := mergeConfigFile(&cfg, path); err != nil {
		return Config{}, err
	}
	cfg.ConfigFile = path
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

	realtimeEnabled := cfg.RealtimeEnabled
	realtimeQueueSize := cfg.RealtimeQueueSize
	realtimeBackoffJitter := cfg.RealtimeBackoffJitter
	raw := fileConfig{
		APIURL:                cfg.APIURL,
		APITLSCAFile:          cfg.APITLSCAFile,
		EnrollmentToken:       cfg.EnrollmentToken,
		NodeID:                cfg.NodeID,
		AgentToken:            cfg.AgentToken,
		HeartbeatInterval:     cfg.HeartbeatInterval.String(),
		MetricsInterval:       cfg.MetricsInterval.String(),
		TaskPollInterval:      cfg.TaskPollInterval.String(),
		RequestTimeout:        cfg.RequestTimeout.String(),
		TaskTimeout:           cfg.TaskTimeout.String(),
		ShutdownTimeout:       cfg.ShutdownTimeout.String(),
		RealtimeEnabled:       &realtimeEnabled,
		RealtimePingInterval:  cfg.RealtimePingInterval.String(),
		RealtimeQueueSize:     &realtimeQueueSize,
		RealtimeBackoffJitter: &realtimeBackoffJitter,
		RealtimeNamespace:     cfg.RealtimeNamespace,
		RealtimePath:          cfg.RealtimePath,
		StateFile:             cfg.StateFile,
		LogLevel:              cfg.LogLevel,
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory for %s: %w", path, err)
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config file %s: %w", path, err)
	}

	if err := writeConfigFile(path, data); err != nil {
		return err
	}

	if mirrorPath := strings.TrimSpace(os.Getenv(configMirrorEnv)); mirrorPath != "" {
		mirrorPath = filepath.Clean(mirrorPath)
		if mirrorPath != filepath.Clean(path) {
			if err := writeConfigFile(mirrorPath, data); err != nil {
				return err
			}
		}
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
	if strings.TrimSpace(c.APITLSCAFile) != "" {
		if _, err := os.Stat(c.APITLSCAFile); err != nil {
			return fmt.Errorf("API_TLS_CA_FILE is invalid: %w", err)
		}
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
	if c.RealtimePingInterval <= 0 {
		return fmt.Errorf("REALTIME_PING_INTERVAL must be greater than zero")
	}
	if c.RealtimeQueueSize <= 0 {
		return fmt.Errorf("REALTIME_QUEUE_SIZE must be greater than zero")
	}
	if c.RealtimeBackoffJitter < 0 || c.RealtimeBackoffJitter > 1 {
		return fmt.Errorf("REALTIME_BACKOFF_JITTER must be between 0 and 1")
	}
	if strings.TrimSpace(c.RealtimeNamespace) == "" {
		return fmt.Errorf("REALTIME_NAMESPACE must not be empty")
	}
	if !strings.HasPrefix(c.RealtimeNamespace, "/") {
		return fmt.Errorf("REALTIME_NAMESPACE must start with '/', got %q", c.RealtimeNamespace)
	}
	if strings.TrimSpace(c.RealtimePath) == "" {
		return fmt.Errorf("REALTIME_PATH must not be empty")
	}
	if !strings.HasPrefix(c.RealtimePath, "/") {
		return fmt.Errorf("REALTIME_PATH must start with '/', got %q", c.RealtimePath)
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
		"/usr/local/etc/noderax-agent/config.json",
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
	if raw.APITLSCAFile != "" {
		cfg.APITLSCAFile = filepath.Clean(strings.TrimSpace(raw.APITLSCAFile))
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
	if raw.RealtimeEnabled != nil {
		cfg.RealtimeEnabled = *raw.RealtimeEnabled
	}
	if raw.RealtimeQueueSize != nil {
		cfg.RealtimeQueueSize = *raw.RealtimeQueueSize
	}
	if raw.RealtimeBackoffJitter != nil {
		cfg.RealtimeBackoffJitter = *raw.RealtimeBackoffJitter
	}
	if raw.RealtimeNamespace != "" {
		cfg.RealtimeNamespace = raw.RealtimeNamespace
	}
	if raw.RealtimePath != "" {
		cfg.RealtimePath = raw.RealtimePath
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
	if err := applyDuration(&cfg.RealtimePingInterval, "realtime_ping_interval", raw.RealtimePingInterval); err != nil {
		return err
	}

	return nil
}

func mergeEnv(cfg *Config) error {
	overrideStringAny(&cfg.APIURL, "NODERAX_API_URL", "API_URL")
	overrideStringAny(&cfg.APITLSCAFile, "NODERAX_API_TLS_CA_FILE", "API_TLS_CA_FILE")
	overrideString(&cfg.EnrollmentToken, "ENROLLMENT_TOKEN")
	overrideString(&cfg.NodeID, "NODE_ID")
	overrideString(&cfg.AgentToken, "AGENT_TOKEN")
	overrideString(&cfg.StateFile, "STATE_FILE")
	overrideString(&cfg.LogLevel, "LOG_LEVEL")
	overrideStringAny(&cfg.RealtimeNamespace, "NODERAX_REALTIME_NAMESPACE", "REALTIME_NAMESPACE")
	overrideStringAny(&cfg.RealtimePath, "NODERAX_REALTIME_PATH", "REALTIME_PATH")
	if err := overrideBool(&cfg.RealtimeEnabled, "REALTIME_ENABLED"); err != nil {
		return err
	}

	if err := overrideDuration(&cfg.HeartbeatInterval, "HEARTBEAT_INTERVAL"); err != nil {
		return err
	}
	if err := overrideDurationAny(&cfg.MetricsInterval, "NODERAX_REALTIME_METRICS_INTERVAL", "METRICS_INTERVAL"); err != nil {
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
	if err := overrideDurationAny(&cfg.RealtimePingInterval, "NODERAX_REALTIME_PING_INTERVAL", "REALTIME_PING_INTERVAL"); err != nil {
		return err
	}
	if err := overrideInt(&cfg.RealtimeQueueSize, "REALTIME_QUEUE_SIZE"); err != nil {
		return err
	}
	if err := overrideFloat(&cfg.RealtimeBackoffJitter, "REALTIME_BACKOFF_JITTER"); err != nil {
		return err
	}

	if cfg.StateFile != "" {
		cfg.StateFile = filepath.Clean(cfg.StateFile)
	}
	if cfg.APITLSCAFile != "" {
		cfg.APITLSCAFile = filepath.Clean(cfg.APITLSCAFile)
	}
	if cfg.RealtimeNamespace != "" && !strings.HasPrefix(cfg.RealtimeNamespace, "/") {
		cfg.RealtimeNamespace = "/" + cfg.RealtimeNamespace
	}
	if cfg.RealtimePath != "" {
		if !strings.HasPrefix(cfg.RealtimePath, "/") {
			cfg.RealtimePath = "/" + cfg.RealtimePath
		}
		if !strings.HasSuffix(cfg.RealtimePath, "/") {
			cfg.RealtimePath += "/"
		}
	}

	return nil
}

func writeConfigFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory for %s: %w", path, err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config file %s: %w", path, err)
	}

	return nil
}

func overrideString(target *string, key string) {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		*target = value
	}
}

func overrideStringAny(target *string, keys ...string) {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			*target = value
			return
		}
	}
}

func overrideDuration(target *time.Duration, key string) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	return applyDuration(target, key, value)
}

func overrideDurationAny(target *time.Duration, keys ...string) error {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		return applyDuration(target, key, value)
	}
	return nil
}

func overrideBool(target *bool, key string) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}

	*target = parsed
	return nil
}

func overrideInt(target *int, key string) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}

	*target = parsed
	return nil
}

func overrideFloat(target *float64, key string) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}

	*target = parsed
	return nil
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

	return strings.TrimRight("https://"+value, "/")
}
