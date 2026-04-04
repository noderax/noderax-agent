package rootaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

const linuxRootProfileHelperPath = "/usr/local/libexec/noderax-agent-root-profile"

type state struct {
	AppliedProfile api.RootAccessProfile `json:"appliedProfile"`
	LastAppliedAt  string                `json:"lastAppliedAt,omitempty"`
	LastError      string                `json:"lastError,omitempty"`
}

type Manager struct {
	logger    *slog.Logger
	statePath string

	mu    sync.RWMutex
	state state
}

func NewManager(identityStatePath string, logger *slog.Logger) *Manager {
	manager := &Manager{
		logger:    logger,
		statePath: buildStatePath(identityStatePath),
		state: state{
			AppliedProfile: api.RootAccessProfileOff,
		},
	}
	manager.load()
	return manager
}

func (m *Manager) BuildAgentReport() *api.RootAccessAgentReport {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return &api.RootAccessAgentReport{
		AppliedProfile: m.state.AppliedProfile,
		LastAppliedAt:  m.state.LastAppliedAt,
		LastError:      m.state.LastError,
	}
}

func (m *Manager) HandleDesiredSnapshot(
	ctx context.Context,
	snapshot *api.RootAccessDesiredSnapshot,
) {
	if snapshot == nil || strings.TrimSpace(string(snapshot.Profile)) == "" {
		return
	}

	m.mu.RLock()
	current := m.state
	m.mu.RUnlock()

	if current.AppliedProfile == snapshot.Profile && strings.TrimSpace(current.LastError) == "" {
		return
	}

	if err := m.applyProfile(ctx, snapshot.Profile); err != nil {
		m.logger.Warn(
			"root access profile sync failed",
			"profile",
			snapshot.Profile,
			"error",
			err,
		)
	}
}

func (m *Manager) CanUseRootScope(scope string) bool {
	profile := m.appliedProfile()

	switch strings.TrimSpace(scope) {
	case "task":
		return profile == api.RootAccessProfileTask || profile == api.RootAccessProfileAll
	case "operational":
		return profile == api.RootAccessProfileOperational || profile == api.RootAccessProfileAll
	default:
		return false
	}
}

func (m *Manager) CanStartRootTerminal() bool {
	profile := m.appliedProfile()
	return profile == api.RootAccessProfileTerminal || profile == api.RootAccessProfileAll
}

func (m *Manager) appliedProfile() api.RootAccessProfile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.AppliedProfile
}

func (m *Manager) applyProfile(
	ctx context.Context,
	profile api.RootAccessProfile,
) error {
	if runtime.GOOS != "linux" {
		if profile == api.RootAccessProfileOff {
			return m.updateState(func(next *state) {
				next.AppliedProfile = profile
				next.LastAppliedAt = time.Now().UTC().Format(time.RFC3339)
				next.LastError = ""
			})
		}

		err := fmt.Errorf("root access profiles are only supported on linux hosts")
		_ = m.updateState(func(next *state) {
			next.LastError = err.Error()
		})
		return err
	}

	helperPath := filepath.Clean(linuxRootProfileHelperPath)
	if _, err := os.Stat(helperPath); err != nil {
		if profile == api.RootAccessProfileOff {
			return m.updateState(func(next *state) {
				next.AppliedProfile = profile
				next.LastAppliedAt = time.Now().UTC().Format(time.RFC3339)
				next.LastError = ""
			})
		}

		stateErr := fmt.Errorf("root profile helper is not installed")
		_ = m.updateState(func(next *state) {
			next.LastError = stateErr.Error()
		})
		return stateErr
	}

	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.CommandContext(ctx, helperPath, "apply", string(profile))
	} else {
		cmd = exec.CommandContext(ctx, "sudo", "-n", helperPath, "apply", string(profile))
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		stateErr := fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		_ = m.updateState(func(next *state) {
			next.LastError = strings.TrimSpace(stateErr.Error())
		})
		return stateErr
	}

	return m.updateState(func(next *state) {
		next.AppliedProfile = profile
		next.LastAppliedAt = time.Now().UTC().Format(time.RFC3339)
		next.LastError = ""
	})
}

func (m *Manager) load() {
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}

	var loaded state
	if err := json.Unmarshal(data, &loaded); err != nil {
		m.logger.Warn("failed to decode root access state", "path", m.statePath, "error", err)
		return
	}

	if strings.TrimSpace(string(loaded.AppliedProfile)) == "" {
		loaded.AppliedProfile = api.RootAccessProfileOff
	}

	m.state = loaded
}

func (m *Manager) updateState(mutator func(*state)) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	next := m.state
	mutator(&next)

	if strings.TrimSpace(string(next.AppliedProfile)) == "" {
		next.AppliedProfile = api.RootAccessProfileOff
	}

	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o755); err != nil {
		return fmt.Errorf("create root access state directory: %w", err)
	}

	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("encode root access state: %w", err)
	}

	if err := os.WriteFile(m.statePath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write root access state: %w", err)
	}

	m.state = next
	return nil
}

func buildStatePath(identityStatePath string) string {
	cleanPath := filepath.Clean(strings.TrimSpace(identityStatePath))
	if cleanPath == "." || cleanPath == "" {
		return "root_access_state.json"
	}

	return filepath.Join(filepath.Dir(cleanPath), "root_access_state.json")
}
