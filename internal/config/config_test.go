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
	if cfg.RealtimeNamespace != "/agent-realtime" {
		t.Fatalf("unexpected realtime namespace default: got=%q want=/agent-realtime", cfg.RealtimeNamespace)
	}
	if cfg.RealtimePath != "/socket.io/" {
		t.Fatalf("unexpected realtime path default: got=%q want=/socket.io/", cfg.RealtimePath)
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

	cfg = Default()
	cfg.APIURL = "https://api.example.com"
	cfg.RealtimeNamespace = "agent-realtime"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for realtime namespace")
	}

	cfg = Default()
	cfg.APIURL = "https://api.example.com"
	cfg.RealtimePath = "socket.io"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for realtime path")
	}
}

func TestLoadLocationConfigFromEnv(t *testing.T) {
	t.Setenv("NODERAX_API_URL", "https://api.example.com")
	t.Setenv("NODERAX_LOCATION_MANUAL_REGION", "Istanbul Home Lab")
	t.Setenv("NODERAX_LOCATION_MANUAL_ZONE", "Rack 1")
	t.Setenv("NODERAX_LOCATION_MANUAL_LATITUDE", "41.0082")
	t.Setenv("NODERAX_LOCATION_MANUAL_LONGITUDE", "28.9784")
	t.Setenv("NODERAX_LOCATION_PUBLIC_IP_ENABLED", "true")
	t.Setenv("NODERAX_IPINFO_TOKEN", "token-123")

	cfg := Default()
	if err := mergeEnv(&cfg); err != nil {
		t.Fatalf("mergeEnv() error = %v", err)
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if cfg.LocationManualRegion != "Istanbul Home Lab" {
		t.Fatalf("manual region = %q, want Istanbul Home Lab", cfg.LocationManualRegion)
	}
	if cfg.LocationManualLatitude == nil || *cfg.LocationManualLatitude != 41.0082 {
		t.Fatalf("manual latitude = %v, want 41.0082", cfg.LocationManualLatitude)
	}
	if cfg.LocationManualLongitude == nil || *cfg.LocationManualLongitude != 28.9784 {
		t.Fatalf("manual longitude = %v, want 28.9784", cfg.LocationManualLongitude)
	}
	if !cfg.LocationPublicIPEnabled {
		t.Fatal("expected public IP location fallback to be enabled")
	}
	if cfg.IPInfoToken != "token-123" {
		t.Fatalf("ipinfo token = %q, want token-123", cfg.IPInfoToken)
	}
}

func TestValidateManualLocationRequiresCoordinates(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.APIURL = "https://api.example.com"
	cfg.LocationManualRegion = "Istanbul"

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for incomplete manual location")
	}
}

func TestNormalizeAPIURLDefaultsToHTTPS(t *testing.T) {
	t.Parallel()

	if got := normalizeAPIURL("api.example.com"); got != "https://api.example.com" {
		t.Fatalf("unexpected normalized URL: got=%q", got)
	}
}
