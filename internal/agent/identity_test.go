package agent

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIdentityStoreSaveLoad(t *testing.T) {
	t.Parallel()

	store := NewIdentityStore(filepath.Join(t.TempDir(), "agent_identity.json"))
	expected := Identity{
		NodeID:       "node-123",
		AgentToken:   "token-abc",
		RegisteredAt: time.Now().UTC().Round(time.Second),
	}

	if err := store.Save(expected); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	actual, err := store.Load()
	if err != nil {
		t.Fatalf("load identity: %v", err)
	}

	if actual.NodeID != expected.NodeID {
		t.Fatalf("node id mismatch: got %q want %q", actual.NodeID, expected.NodeID)
	}
	if actual.AgentToken != expected.AgentToken {
		t.Fatalf("agent token mismatch: got %q want %q", actual.AgentToken, expected.AgentToken)
	}
	if !actual.RegisteredAt.Equal(expected.RegisteredAt) {
		t.Fatalf("registered at mismatch: got %s want %s", actual.RegisteredAt, expected.RegisteredAt)
	}
}
