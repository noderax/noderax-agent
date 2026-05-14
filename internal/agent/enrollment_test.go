package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/config"
)

func TestRunInteractiveEnrollmentUsesExpectedInitiatePayload(t *testing.T) {
	tmpDir := t.TempDir()
	originalDetectNodeLocation := detectNodeLocation
	detectNodeLocation = func(context.Context, config.Config, *slog.Logger) *api.NodeLocation {
		return &api.NodeLocation{
			Provider: "aws",
			Source:   "cloud_metadata",
			Region:   "eu-central-1",
			Zone:     "eu-central-1a",
		}
	}
	t.Cleanup(func() {
		detectNodeLocation = originalDetectNodeLocation
	})

	cfg := config.Config{
		APIURL:            "https://api.example.com",
		HeartbeatInterval: 30 * time.Second,
		MetricsInterval:   60 * time.Second,
		TaskPollInterval:  15 * time.Second,
		RequestTimeout:    50 * time.Millisecond,
		TaskTimeout:       10 * time.Minute,
		ShutdownTimeout:   20 * time.Second,
		StateFile:         filepath.Join(tmpDir, "agent_identity.json"),
		ConfigFile:        filepath.Join(tmpDir, "config.json"),
		LogLevel:          "info",
	}

	client := &fakeInteractiveEnrollmentClient{
		initiateResponse: api.InitiateEnrollmentResponse{
			Token:     "short-lived-enrollment-token",
			ExpiresAt: time.Now().Add(time.Minute).UTC(),
		},
		statuses: []api.EnrollmentStatusResponse{
			{Status: "approved", NodeID: "node-555", AgentToken: "agent-token-555"},
		},
	}

	var output bytes.Buffer
	err := RunInteractiveEnrollment(
		context.Background(),
		cfg,
		client,
		discardLogger(),
		"dev",
		strings.NewReader("admin@example.com\n"),
		&output,
	)
	if err != nil {
		t.Fatalf("RunInteractiveEnrollment returned error: %v", err)
	}

	if client.initiateRequest.Email != "admin@example.com" {
		t.Fatalf("email mismatch: got %q want %q", client.initiateRequest.Email, "admin@example.com")
	}
	if client.initiateRequest.Hostname == "" {
		t.Fatal("expected hostname to be populated")
	}
	if client.initiateRequest.AdditionalInfo.OS == "" {
		t.Fatal("expected additionalInfo.os to be populated")
	}
	if client.initiateRequest.AdditionalInfo.Arch == "" {
		t.Fatal("expected additionalInfo.arch to be populated")
	}
	if client.initiateRequest.AdditionalInfo.AgentVersion != "dev" {
		t.Fatalf("agent version mismatch: got %q want %q", client.initiateRequest.AdditionalInfo.AgentVersion, "dev")
	}
	if client.initiateRequest.AdditionalInfo.Location == nil {
		t.Fatal("expected node location to be included")
	}
	if client.initiateRequest.AdditionalInfo.Location.Region != "eu-central-1" {
		t.Fatalf("location region mismatch: got %q want %q", client.initiateRequest.AdditionalInfo.Location.Region, "eu-central-1")
	}
}

func TestWaitForEnrollmentApprovalReturnsIdentity(t *testing.T) {
	t.Parallel()

	client := &fakeEnrollmentStatusClient{
		responses: []api.EnrollmentStatusResponse{
			{Status: "pending"},
			{Status: "approved", NodeID: "node-123", AgentToken: "agent-token-123"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	identity, err := waitForEnrollmentApproval(ctx, client, discardLogger(), 50*time.Millisecond, "pending-token", time.Millisecond)
	if err != nil {
		t.Fatalf("waitForEnrollmentApproval returned error: %v", err)
	}
	if identity.NodeID != "node-123" {
		t.Fatalf("node id mismatch: got %q want %q", identity.NodeID, "node-123")
	}
	if identity.AgentToken != "agent-token-123" {
		t.Fatalf("agent token mismatch: got %q want %q", identity.AgentToken, "agent-token-123")
	}
	if client.calls < 2 {
		t.Fatalf("expected multiple polls, got %d", client.calls)
	}
}

func TestWaitForEnrollmentApprovalRejectsRevoked(t *testing.T) {
	t.Parallel()

	client := &fakeEnrollmentStatusClient{
		responses: []api.EnrollmentStatusResponse{
			{Status: "revoked"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := waitForEnrollmentApproval(ctx, client, discardLogger(), 50*time.Millisecond, "pending-token", time.Millisecond)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompleteEnrollmentPersistsIdentityAndClearsToken(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfg := config.Config{
		APIURL:            "https://api.example.com",
		EnrollmentToken:   "pending-token",
		HeartbeatInterval: 30 * time.Second,
		MetricsInterval:   60 * time.Second,
		TaskPollInterval:  15 * time.Second,
		RequestTimeout:    50 * time.Millisecond,
		TaskTimeout:       10 * time.Minute,
		ShutdownTimeout:   20 * time.Second,
		StateFile:         filepath.Join(tmpDir, "agent_identity.json"),
		ConfigFile:        filepath.Join(tmpDir, "config.json"),
		LogLevel:          "info",
	}
	if err := config.SaveFile(cfg.ConfigFile, cfg); err != nil {
		t.Fatalf("SaveFile returned error: %v", err)
	}

	store := NewIdentityStore(cfg.StateFile)
	client := &fakeEnrollmentStatusClient{
		responses: []api.EnrollmentStatusResponse{
			{Status: "approved", NodeID: "node-789", AgentToken: "agent-token-789"},
		},
	}

	identity, err := completeEnrollment(context.Background(), cfg, client, discardLogger(), store, cfg.EnrollmentToken, time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("completeEnrollment returned error: %v", err)
	}
	if identity.NodeID != "node-789" || identity.AgentToken != "agent-token-789" {
		t.Fatalf("unexpected identity: %+v", identity)
	}

	persistedIdentity, err := store.Load()
	if err != nil {
		t.Fatalf("failed to load identity: %v", err)
	}
	if persistedIdentity.NodeID != "node-789" {
		t.Fatalf("persisted node id mismatch: got %q want %q", persistedIdentity.NodeID, "node-789")
	}
	if persistedIdentity.AgentToken != "agent-token-789" {
		t.Fatalf("persisted agent token mismatch: got %q want %q", persistedIdentity.AgentToken, "agent-token-789")
	}

	configData := struct {
		EnrollmentToken string `json:"enrollment_token"`
	}{}
	rawConfig, err := os.ReadFile(cfg.ConfigFile)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	if err := json.Unmarshal(rawConfig, &configData); err != nil {
		t.Fatalf("failed to decode config file: %v", err)
	}
	if configData.EnrollmentToken != "" {
		t.Fatalf("expected enrollment token to be cleared, got %q", configData.EnrollmentToken)
	}
}

type fakeEnrollmentStatusClient struct {
	responses []api.EnrollmentStatusResponse
	calls     int
}

func (f *fakeEnrollmentStatusClient) GetEnrollment(_ context.Context, _ string) (api.EnrollmentStatusResponse, error) {
	if len(f.responses) == 0 {
		return api.EnrollmentStatusResponse{Status: "pending"}, nil
	}

	index := f.calls
	if index >= len(f.responses) {
		index = len(f.responses) - 1
	}
	f.calls++

	return f.responses[index], nil
}

type fakeInteractiveEnrollmentClient struct {
	initiateRequest  api.InitiateEnrollmentRequest
	initiateResponse api.InitiateEnrollmentResponse
	statuses         []api.EnrollmentStatusResponse
	statusCalls      int
}

func (f *fakeInteractiveEnrollmentClient) InitiateEnrollment(_ context.Context, request api.InitiateEnrollmentRequest) (api.InitiateEnrollmentResponse, error) {
	f.initiateRequest = request
	return f.initiateResponse, nil
}

func (f *fakeInteractiveEnrollmentClient) GetEnrollment(_ context.Context, _ string) (api.EnrollmentStatusResponse, error) {
	if len(f.statuses) == 0 {
		return api.EnrollmentStatusResponse{Status: "pending"}, nil
	}

	index := f.statusCalls
	if index >= len(f.statuses) {
		index = len(f.statuses) - 1
	}
	f.statusCalls++

	return f.statuses[index], nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
