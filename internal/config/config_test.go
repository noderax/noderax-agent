package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveFileMirrorsConfigWhenRequested(t *testing.T) {
	t.Setenv(configMirrorEnv, filepath.Join(t.TempDir(), "mirror", "config.json"))

	targetPath := filepath.Join(t.TempDir(), "managed", "config.json")
	cfg := Default()
	cfg.APIURL = "https://api.example.com"
	cfg.ConfigFile = targetPath

	if err := SaveFile(targetPath, cfg); err != nil {
		t.Fatalf("SaveFile() error = %v", err)
	}

	targetData, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target config: %v", err)
	}

	mirrorPath := os.Getenv(configMirrorEnv)
	mirrorData, err := os.ReadFile(mirrorPath)
	if err != nil {
		t.Fatalf("read mirror config: %v", err)
	}

	if string(targetData) != string(mirrorData) {
		t.Fatalf("mirror config does not match target config")
	}
}

func TestDefaultLowLatencyRealtimeSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	if cfg.RealtimePingInterval != 2*time.Second {
		t.Fatalf("unexpected realtime ping default: got=%s want=2s", cfg.RealtimePingInterval)
	}
	if cfg.MetricsInterval != 2*time.Second {
		t.Fatalf("unexpected metrics interval default: got=%s want=2s", cfg.MetricsInterval)
	}
	if cfg.RealtimeQueueSize != 1024 {
		t.Fatalf("unexpected realtime queue size default: got=%d want=1024", cfg.RealtimeQueueSize)
	}
	if cfg.RealtimeBackoffJitter != 0.2 {
		t.Fatalf("unexpected realtime jitter default: got=%f want=0.2", cfg.RealtimeBackoffJitter)
	}
}

func TestValidateRealtimeKnobs(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.APIURL = "https://api.example.com"
	cfg.RealtimeQueueSize = 0
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for realtime queue size")
	}

	cfg = Default()
	cfg.APIURL = "https://api.example.com"
	cfg.RealtimeBackoffJitter = 1.2
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for realtime jitter")
	}
}
