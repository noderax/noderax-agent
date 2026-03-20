package metrics

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/realtime"
	"github.com/noderax/noderax-agent/internal/system"
)

type Service struct {
	realtime       *realtime.Service
	logger         *slog.Logger
	interval       time.Duration
	requestTimeout time.Duration
	credentials    func() (string, string)
	collector      *system.Collector
}

func NewService(
	realtime *realtime.Service,
	logger *slog.Logger,
	interval time.Duration,
	requestTimeout time.Duration,
	credentials func() (string, string),
) *Service {
	return &Service{
		realtime:       realtime,
		logger:         logger,
		interval:       interval,
		requestTimeout: requestTimeout,
		credentials:    credentials,
		collector:      system.NewCollector(),
	}
}

func (s *Service) Run(ctx context.Context) error {
	s.collectAndSend(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.collectAndSend(ctx)
		}
	}
}

func (s *Service) collectAndSend(ctx context.Context) {
	if s.realtime == nil {
		s.logger.Error("realtime metrics skipped because realtime client is unavailable")
		return
	}

	nodeID, agentToken := s.credentials()
	if nodeID == "" || agentToken == "" {
		s.logger.Warn("metrics skipped because agent identity is missing")
		return
	}

	collectCtx, cancelCollect := context.WithTimeout(ctx, s.requestTimeout)
	defer cancelCollect()

	snapshot, err := s.collector.Collect(collectCtx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.logger.Error("metrics collection failed", "error", err)
		return
	}

	requestCtx, cancelRequest := context.WithTimeout(ctx, s.requestTimeout)
	defer cancelRequest()

	err = s.realtime.SendMetrics(requestCtx, api.MetricsRequest{
		NodeID:      nodeID,
		AgentToken:  agentToken,
		CollectedAt: snapshot.CollectedAt,
		CPU: api.CPUStats{
			UsagePercent: snapshot.CPU.UsagePercent,
		},
		Memory: api.MemoryStats{
			TotalBytes:     snapshot.Memory.TotalBytes,
			UsedBytes:      snapshot.Memory.UsedBytes,
			FreeBytes:      snapshot.Memory.FreeBytes,
			UsedPercent:    snapshot.Memory.UsedPercent,
			AvailableBytes: snapshot.Memory.AvailableBytes,
		},
		Disk: api.DiskStats{
			Path:        snapshot.Disk.Path,
			TotalBytes:  snapshot.Disk.TotalBytes,
			UsedBytes:   snapshot.Disk.UsedBytes,
			FreeBytes:   snapshot.Disk.FreeBytes,
			UsedPercent: snapshot.Disk.UsedPercent,
		},
		Networks: mapNetworks(snapshot.Networks),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.logger.Error("sending realtime metrics failed", "error", err)
		return
	}

	s.logger.Info("realtime metrics sent", "node_id", nodeID)
}

func mapNetworks(items []system.NetworkStats) []api.NetworkStats {
	networks := make([]api.NetworkStats, 0, len(items))
	for _, item := range items {
		networks = append(networks, api.NetworkStats{
			Interface:   item.Interface,
			BytesSent:   item.BytesSent,
			BytesRecv:   item.BytesRecv,
			PacketsSent: item.PacketsSent,
			PacketsRecv: item.PacketsRecv,
			ErrorsIn:    item.ErrorsIn,
			ErrorsOut:   item.ErrorsOut,
			DropIn:      item.DropIn,
			DropOut:     item.DropOut,
		})
	}
	return networks
}
