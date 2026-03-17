package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var ErrIdentityNotFound = errors.New("identity state file not found")

type Identity struct {
	NodeID       string    `json:"node_id"`
	AgentToken   string    `json:"agent_token"`
	RegisteredAt time.Time `json:"registered_at,omitempty"`
}

func (i Identity) Ready() bool {
	return i.NodeID != "" && i.AgentToken != ""
}

type IdentityManager struct {
	mu       sync.RWMutex
	identity Identity
}

func NewIdentityManager(identity Identity) *IdentityManager {
	return &IdentityManager{identity: identity}
}

func (m *IdentityManager) Current() Identity {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.identity
}

func (m *IdentityManager) Set(identity Identity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.identity = identity
}

func (m *IdentityManager) Credentials() (string, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.identity.NodeID, m.identity.AgentToken
}

type IdentityStore struct {
	path string
}

func NewIdentityStore(path string) *IdentityStore {
	return &IdentityStore{path: path}
}

func (s *IdentityStore) Load() (Identity, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Identity{}, ErrIdentityNotFound
		}
		return Identity{}, fmt.Errorf("read identity state %s: %w", s.path, err)
	}

	var identity Identity
	if err := json.Unmarshal(data, &identity); err != nil {
		return Identity{}, fmt.Errorf("decode identity state %s: %w", s.path, err)
	}

	return identity, nil
}

func (s *IdentityStore) Save(identity Identity) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create state directory for %s: %w", s.path, err)
	}

	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("encode identity state: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write identity state %s: %w", s.path, err)
	}

	return nil
}
