package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/config"
	"github.com/noderax/noderax-agent/internal/heartbeat"
	"github.com/noderax/noderax-agent/internal/metrics"
	"github.com/noderax/noderax-agent/internal/system"
	"github.com/noderax/noderax-agent/internal/tasks"
)

type Service struct {
	cfg       config.Config
	client    *api.Client
	logger    *slog.Logger
	version   string
	identity  *IdentityManager
	store     *IdentityStore
	heartbeat *heartbeat.Service
	metrics   *metrics.Service
	tasks     *tasks.Service
}

func NewService(cfg config.Config, client *api.Client, logger *slog.Logger, version string) *Service {
	identity := NewIdentityManager(Identity{
		NodeID:     cfg.NodeID,
		AgentToken: cfg.AgentToken,
	})

	return &Service{
		cfg:      cfg,
		client:   client,
		logger:   logger,
		version:  version,
		identity: identity,
		store:    NewIdentityStore(cfg.StateFile),
		heartbeat: heartbeat.NewService(
			client,
			logger,
			cfg.HeartbeatInterval,
			cfg.RequestTimeout,
			identity.Credentials,
			version,
		),
		metrics: metrics.NewService(
			client,
			logger,
			cfg.MetricsInterval,
			cfg.RequestTimeout,
			identity.Credentials,
		),
		tasks: tasks.NewService(
			client,
			logger,
			cfg.TaskPollInterval,
			cfg.RequestTimeout,
			cfg.TaskTimeout,
			identity.Credentials,
		),
	}
}

func (s *Service) Run(ctx context.Context) error {
	if err := s.bootstrapIdentity(ctx); err != nil {
		return err
	}

	currentIdentity := s.identity.Current()
	s.logger.Info("agent identity ready", "node_id", currentIdentity.NodeID, "state_file", s.cfg.StateFile)

	workers := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "heartbeat", run: s.heartbeat.Run},
		{name: "metrics", run: s.metrics.Run},
		{name: "tasks", run: s.tasks.Run},
	}

	var wg sync.WaitGroup
	for _, worker := range workers {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := worker.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Error("worker exited with error", "worker", worker.name, "error", err)
			}
		}()
	}

	<-ctx.Done()
	s.logger.Info("shutdown requested")

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(s.cfg.ShutdownTimeout):
		s.logger.Warn("graceful shutdown timed out", "timeout", s.cfg.ShutdownTimeout)
		return nil
	}
}

func (s *Service) bootstrapIdentity(ctx context.Context) error {
	if current := s.identity.Current(); current.Ready() {
		s.client.SetAgentToken(current.AgentToken)
		return nil
	}

	if identity, err := s.store.Load(); err == nil && identity.Ready() {
		s.identity.Set(identity)
		s.client.SetAgentToken(identity.AgentToken)
		s.logger.Info("loaded persisted agent identity", "node_id", identity.NodeID, "path", s.cfg.StateFile)
		return nil
	} else if err != nil && !errors.Is(err, ErrIdentityNotFound) {
		return err
	}

	hostInfo, err := system.HostInfo(ctx)
	if err != nil {
		s.logger.Warn("host info collection returned partial data", "error", err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()

	response, err := s.client.Register(requestCtx, api.RegisterRequest{
		EnrollmentToken: s.cfg.EnrollmentToken,
		Hostname:        hostInfo.Hostname,
		OperatingSystem: hostInfo.OS,
		Platform:        hostInfo.Platform,
		PlatformVersion: hostInfo.PlatformVersion,
		KernelVersion:   hostInfo.KernelVersion,
		Architecture:    hostInfo.Architecture,
		AgentVersion:    s.version,
	})
	if err != nil {
		return fmt.Errorf("register agent: %w", err)
	}

	identity := Identity{
		NodeID:       response.NodeID,
		AgentToken:   response.AgentToken,
		RegisteredAt: time.Now().UTC(),
	}

	s.identity.Set(identity)
	s.client.SetAgentToken(identity.AgentToken)

	if err := s.store.Save(identity); err != nil {
		return fmt.Errorf("persist identity: %w", err)
	}

	s.logger.Info("agent registered successfully", "node_id", identity.NodeID)
	return nil
}
