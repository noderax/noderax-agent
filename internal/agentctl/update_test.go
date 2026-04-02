package agentctl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseUpdateOptionsConsumesRequestFile(t *testing.T) {
	t.Parallel()

	requestPath := filepath.Join(t.TempDir(), "update-request.json")
	if err := os.WriteFile(
		requestPath,
		[]byte(`{"targetVersion":"1.0.1","targetId":"target-42","rollback":true}`),
		0o600,
	); err != nil {
		t.Fatalf("write request file: %v", err)
	}

	options, err := parseUpdateOptions([]string{"--request-file", requestPath, "--apply-now"})
	if err != nil {
		t.Fatalf("parseUpdateOptions returned error: %v", err)
	}

	if options.TargetVersion != "1.0.1" {
		t.Fatalf("target version mismatch: got %q", options.TargetVersion)
	}
	if options.TargetID != "target-42" {
		t.Fatalf("target id mismatch: got %q", options.TargetID)
	}
	if !options.Rollback {
		t.Fatal("expected rollback flag to be restored from request file")
	}
	if !options.ApplyNow {
		t.Fatal("expected apply-now to be preserved")
	}
	if _, err := os.Stat(requestPath); !os.IsNotExist(err) {
		t.Fatalf("expected request file to be consumed, stat err=%v", err)
	}
}

func TestParseUpdateOptionsRejectsMixedRequestFileFlags(t *testing.T) {
	t.Parallel()

	requestPath := filepath.Join(t.TempDir(), "update-request.json")
	if err := os.WriteFile(
		requestPath,
		[]byte(`{"targetVersion":"1.0.1","targetId":"target-42"}`),
		0o600,
	); err != nil {
		t.Fatalf("write request file: %v", err)
	}

	if _, err := parseUpdateOptions([]string{
		"--request-file", requestPath,
		"--target-version", "1.0.2",
	}); err == nil {
		t.Fatal("expected mixed request-file flags to be rejected")
	}
}
