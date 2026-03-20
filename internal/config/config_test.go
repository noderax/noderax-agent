package config

import (
	"os"
	"path/filepath"
	"testing"
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
