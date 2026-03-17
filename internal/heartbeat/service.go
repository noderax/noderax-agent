package heartbeat

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

type Service struct {
	client         *api.Client
	logger         *slog.Logger
	interval       time.Duration
	requestTimeout time.Duration
	credentials    func() (string, string)
	version        string
}

func NewService(
	client *api.Client,
	logger *slog.Logger,
	interval time.Duration,
	requestTimeout time.Duration,
	credentials func() (string, string),
	version string,
) *Service {
	return &Service{
		client:         client,
		logger:         logger,
		interval:       interval,
		requestTimeout: requestTimeout,
		credentials:    credentials,
		version:        version,
	}
}

func (s *Service) Run(ctx context.Context) error {
	s.send(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.send(ctx)
		}
	}
}

func (s *Service) send(ctx context.Context) {
	nodeID, agentToken := s.credentials()
	if nodeID == "" || agentToken == "" {
		s.logger.Warn("heartbeat skipped because agent identity is missing")
		return
	}

	requestCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()

	err := s.client.Heartbeat(requestCtx, api.HeartbeatRequest{
		NodeID:       nodeID,
		AgentToken:   agentToken,
		AgentVersion: s.version,
		SentAt:       time.Now().UTC(),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.logger.Error("heartbeat failed", "error", err)
		return
	}

	s.logger.Info("heartbeat sent", "node_id", nodeID)
}
