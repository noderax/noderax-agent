package rootaccess

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/noderax/noderax-agent/internal/api"
)

func TestHandleDesiredSnapshotReappliesSameProfileForLegacyStateRevision(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "root_access_state.json")
	legacyState := state{
		AppliedProfile: api.RootAccessProfileOperational,
		LastAppliedAt:  "2026-04-06T12:00:00Z",
		LastError:      "",
	}
	writeStateFile(t, statePath, legacyState)

	manager := NewManager(filepath.Join(tempDir, "identity.json"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	applyCalls := 0
	manager.applyFunc = func(_ context.Context, profile api.RootAccessProfile) error {
		applyCalls++
		return manager.updateState(func(next *state) {
			next.AppliedProfile = profile
			next.LastAppliedAt = "2026-04-06T12:05:00Z"
			next.LastError = ""
		})
	}

	manager.HandleDesiredSnapshot(context.Background(), &api.RootAccessDesiredSnapshot{
		Profile: api.RootAccessProfileOperational,
	})

	if applyCalls != 1 {
		t.Fatalf("expected one reconcile apply, got %d", applyCalls)
	}

	report := manager.BuildAgentReport()
	if report.AppliedProfile != api.RootAccessProfileOperational {
		t.Fatalf("applied profile mismatch: got %q", report.AppliedProfile)
	}

	var persisted state
	readStateFile(t, statePath, &persisted)
	if persisted.Revision != rootAccessStateRevision {
		t.Fatalf("expected persisted revision %d, got %d", rootAccessStateRevision, persisted.Revision)
	}

	manager.HandleDesiredSnapshot(context.Background(), &api.RootAccessDesiredSnapshot{
		Profile: api.RootAccessProfileOperational,
	})

	if applyCalls != 1 {
		t.Fatalf("expected no second apply after reconcile, got %d", applyCalls)
	}
}

func TestHandleDesiredSnapshotSkipsWhenProfileAndRevisionAreCurrent(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "root_access_state.json")
	currentState := state{
		AppliedProfile: api.RootAccessProfileOperational,
		LastAppliedAt:  "2026-04-06T12:00:00Z",
		LastError:      "",
		Revision:       rootAccessStateRevision,
	}
	writeStateFile(t, statePath, currentState)

	manager := NewManager(filepath.Join(tempDir, "identity.json"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	applyCalls := 0
	manager.applyFunc = func(_ context.Context, _ api.RootAccessProfile) error {
		applyCalls++
		return nil
	}

	manager.HandleDesiredSnapshot(context.Background(), &api.RootAccessDesiredSnapshot{
		Profile: api.RootAccessProfileOperational,
	})

	if applyCalls != 0 {
		t.Fatalf("expected no reconcile apply, got %d", applyCalls)
	}
}

func writeStateFile(t *testing.T, path string, value state) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
}

func readStateFile(t *testing.T, path string, target *state) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode state file: %v", err)
	}
}
