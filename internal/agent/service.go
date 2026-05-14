package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/config"
	"github.com/noderax/noderax-agent/internal/metrics"
	"github.com/noderax/noderax-agent/internal/nodelocation"
	"github.com/noderax/noderax-agent/internal/realtime"
	"github.com/noderax/noderax-agent/internal/rootaccess"
	"github.com/noderax/noderax-agent/internal/system"
	"github.com/noderax/noderax-agent/internal/tasks"
	"github.com/noderax/noderax-agent/internal/terminal"
)

type Service struct {
	cfg      config.Config
	client   *api.Client
	logger   *slog.Logger
	version  string
	identity *IdentityManager
	store    *IdentityStore
	metrics  *metrics.Service
	root     *rootaccess.Manager
	tasks    *tasks.Service
	terminal *terminal.Manager
	realtime *realtime.Service
	initErr  error
}

type terminalController interface {
	StartSession(context.Context, string, int, int, bool) error
	WriteInput(context.Context, string, string) error
	ResizeSession(context.Context, string, int, int) error
	StopSession(context.Context, string, string) error
}

type realtimeCommandHandler struct {
	tasks    *tasks.Service
	terminal terminalController
}

func (h *realtimeCommandHandler) DispatchRealtimeTask(ctx context.Context, task api.Task) bool {
	if h.tasks == nil {
		return false
	}

	return h.tasks.DispatchRealtimeTask(ctx, task)
}

func (h *realtimeCommandHandler) StartTerminalSession(
	ctx context.Context,
	sessionID string,
	cols int,
	rows int,
	runAsRoot bool,
) error {
	if h.terminal == nil {
		return fmt.Errorf("terminal manager is not configured")
	}

	return h.terminal.StartSession(ctx, sessionID, cols, rows, runAsRoot)
}

func (h *realtimeCommandHandler) WriteTerminalInput(
	ctx context.Context,
	sessionID string,
	payload string,
) error {
	if h.terminal == nil {
		return fmt.Errorf("terminal manager is not configured")
	}

	return h.terminal.WriteInput(ctx, sessionID, payload)
}

func (h *realtimeCommandHandler) ResizeTerminalSession(
	ctx context.Context,
	sessionID string,
	cols int,
	rows int,
) error {
	if h.terminal == nil {
		return fmt.Errorf("terminal manager is not configured")
	}

	return h.terminal.ResizeSession(ctx, sessionID, cols, rows)
}

func (h *realtimeCommandHandler) StopTerminalSession(
	ctx context.Context,
	sessionID string,
	reason string,
) error {
	if h.terminal == nil {
		return fmt.Errorf("terminal manager is not configured")
	}

	return h.terminal.StopSession(ctx, sessionID, reason)
}

func NewService(cfg config.Config, client *api.Client, logger *slog.Logger, version string) *Service {
	identity := NewIdentityManager(Identity{
		NodeID:     cfg.NodeID,
		AgentToken: cfg.AgentToken,
	})
	if !cfg.RealtimeEnabled {
		logger.Warn("realtime mode is mandatory; ignoring realtime_enabled=false and continuing with realtime")
	}

	taskService := tasks.NewService(
		logger,
		cfg.RequestTimeout,
		cfg.TaskTimeout,
		identity.Credentials,
	)
	rootManager := rootaccess.NewManager(cfg.StateFile, logger)
	taskService.SetTaskPollingClient(client, cfg.TaskPollInterval)
	taskService.SetTaskControlClient(client)
	taskService.SetTaskAuthClient(client)
	taskService.SetRealtimeEvents(tasks.NewHTTPTaskEvents(client, logger))
	taskService.SetRootAccessController(rootManager)

	metricsService := metrics.NewService(
		nil,
		logger,
		cfg.MetricsInterval,
		cfg.RequestTimeout,
		identity.Credentials,
	)

	commandHandler := &realtimeCommandHandler{tasks: taskService}
	realtimeService, err := realtime.NewService(
		cfg.APIURL,
		cfg.RealtimeNamespace,
		cfg.RealtimePath,
		cfg.RequestTimeout,
		cfg.RealtimePingInterval,
		cfg.RealtimeQueueSize,
		cfg.RealtimeBackoffJitter,
		logger,
		identity.Credentials,
		commandHandler,
		rootManager.BuildAgentReport,
		func(ctx context.Context, ack realtime.AuthAckPayload) {
			rootManager.HandleDesiredSnapshot(ctx, ack.RootAccess)
			metricsService.TriggerImmediateSnapshot()
		},
	)
	if err != nil {
		logger.Error("failed to initialize realtime service", "error", err)
	}
	if realtimeService != nil {
		realtimeService.SetRuntimeAgentVersion(version)
		setRealtimeNodeLocation(context.Background(), cfg, logger, realtimeService, false)
		hostInfo, hostInfoErr := system.HostInfo(context.Background())
		if hostInfoErr != nil {
			logger.Warn("failed to read host metadata for realtime auth", "error", hostInfoErr)
		} else {
			realtimeService.SetRuntimePlatformVersion(hostInfo.PlatformVersion)
			realtimeService.SetRuntimeKernelVersion(hostInfo.KernelVersion)
		}
	}

	var terminalEvents terminal.RealtimeEvents
	if realtimeService != nil {
		terminalEvents = realtimeService
	}
	terminalManager := terminal.NewManager(logger, terminalEvents)
	terminalManager.SetRootTerminalChecker(rootManager.CanStartRootTerminal)
	commandHandler.terminal = terminalManager
	metricsService.SetRealtimeClient(realtimeService)

	return &Service{
		cfg:      cfg,
		client:   client,
		logger:   logger,
		version:  version,
		identity: identity,
		store:    NewIdentityStore(cfg.StateFile),
		metrics:  metricsService,
		root:     rootManager,
		tasks:    taskService,
		terminal: terminalManager,
		realtime: realtimeService,
		initErr:  err,
	}
}

func (s *Service) Run(ctx context.Context) error {
	if s.initErr != nil {
		return s.initErr
	}

	if err := s.bootstrapIdentity(ctx); err != nil {
		return err
	}

	currentIdentity := s.identity.Current()
	s.logger.Info("agent identity ready", "node_id", currentIdentity.NodeID, "state_file", s.cfg.StateFile)

	workers := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "node-location", run: s.runNodeLocationSync},
		{name: "realtime", run: s.realtime.Run},
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

func (s *Service) runNodeLocationSync(ctx context.Context) error {
	if s.realtime == nil || s.realtime.RuntimeLocation() != nil {
		return nil
	}

	delays := []time.Duration{
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		5 * time.Minute,
		15 * time.Minute,
	}
	for _, delay := range delays {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}

		if setRealtimeNodeLocation(ctx, s.cfg, s.logger, s.realtime, true) {
			return nil
		}
	}

	return nil
}

func setRealtimeNodeLocation(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	realtimeService *realtime.Service,
	refreshAuth bool,
) bool {
	location, err := nodelocation.Detect(ctx, cfg)
	if err != nil {
		if logger != nil {
			logger.Debug("node location detection skipped", "error", err)
		}
		return false
	}
	if location == nil {
		return false
	}

	realtimeService.SetRuntimeLocation(location)
	if logger != nil {
		logger.Info(
			"node location detected",
			"provider", location.Provider,
			"source", location.Source,
			"region", location.Region,
			"zone", location.Zone,
		)
	}
	if refreshAuth {
		refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := realtimeService.RefreshAuth(refreshCtx); err != nil && !errors.Is(err, realtime.ErrSessionNotActive) && logger != nil {
			logger.Debug("node location realtime auth refresh skipped", "error", err)
		}
	}
	return true
}

func (s *Service) bootstrapIdentity(ctx context.Context) error {
	if current := s.identity.Current(); current.Ready() {
		s.client.SetAgentToken(current.AgentToken)
		s.client.SetAgentNodeID(current.NodeID)
		return nil
	}

	if identity, err := s.store.Load(); err == nil && identity.Ready() {
		s.identity.Set(identity)
		s.client.SetAgentToken(identity.AgentToken)
		s.client.SetAgentNodeID(identity.NodeID)
		s.logger.Info("loaded persisted agent identity", "node_id", identity.NodeID, "path", s.cfg.StateFile)
		return nil
	} else if err != nil && !errors.Is(err, ErrIdentityNotFound) {
		return err
	}

	if strings.TrimSpace(s.cfg.EnrollmentToken) == "" {
		return fmt.Errorf("agent identity is missing; run `noderax-agent enroll` to create an enrollment token")
	}

	identity, err := completeEnrollment(
		ctx,
		s.cfg,
		s.client,
		s.logger,
		s.store,
		s.cfg.EnrollmentToken,
		defaultEnrollmentPollInterval,
		defaultEnrollmentWaitTimeout,
	)
	if err != nil {
		return fmt.Errorf("complete enrollment: %w", err)
	}

	s.identity.Set(identity)
	s.client.SetAgentToken(identity.AgentToken)
	s.client.SetAgentNodeID(identity.NodeID)
	s.logger.Info("agent enrollment approved", "node_id", identity.NodeID)
	return nil
}
