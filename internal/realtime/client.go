package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	sio "github.com/karagenc/socket.io-go"
	eio "github.com/karagenc/socket.io-go/engine.io"
	"github.com/karagenc/socket.io-go/engine.io/transport"
	"github.com/noderax/noderax-agent/internal/api"
)

var reconnectDelays = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}

type AuthSuccessHook func(context.Context)

type Stats struct {
	Reconnects      int64
	PingsSent       int64
	MetricsSent     int64
	LifecycleSent   int64
	QueueDrops      int64
	LogDrops        int64
	QueueDepth      int64
	MaxQueueDepth   int64
	DispatchHandled int64
}

type Service struct {
	logger         *slog.Logger
	requestTimeout time.Duration
	pingInterval   time.Duration
	jitterRatio    float64
	credentials    func() (string, string)
	dispatcher     *dispatcher
	onAuthSuccess  AuthSuccessHook
	baseURL        string
	outbound       chan any

	reconnects      atomic.Int64
	pingsSent       atomic.Int64
	metricsSent     atomic.Int64
	lifecycleSent   atomic.Int64
	queueDrops      atomic.Int64
	logDrops        atomic.Int64
	queueDepth      atomic.Int64
	maxQueueDepth   atomic.Int64
	dispatchHandled atomic.Int64
}

type socketIOConn struct {
	manager      *sio.Manager
	socket       sio.ClientSocket
	disconnectCh <-chan error
}

func NewService(
	apiURL string,
	requestTimeout time.Duration,
	pingInterval time.Duration,
	queueSize int,
	jitterRatio float64,
	logger *slog.Logger,
	credentials func() (string, string),
	handler taskDispatcher,
	onAuthSuccess AuthSuccessHook,
) (*Service, error) {
	baseURL, err := realtimeEndpoint(apiURL)
	if err != nil {
		return nil, err
	}
	if queueSize <= 0 {
		queueSize = 1024
	}
	if jitterRatio < 0 {
		jitterRatio = 0
	}
	if jitterRatio > 1 {
		jitterRatio = 1
	}
	if onAuthSuccess == nil {
		onAuthSuccess = func(context.Context) {}
	}

	return &Service{
		logger:         logger,
		requestTimeout: requestTimeout,
		pingInterval:   pingInterval,
		jitterRatio:    jitterRatio,
		credentials:    credentials,
		dispatcher:     newDispatcher(handler),
		onAuthSuccess:  onAuthSuccess,
		baseURL:        baseURL,
		outbound:       make(chan any, queueSize),
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	retryIndex := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		client, err := s.connect(ctx)
		if err != nil {
			s.logger.Warn("realtime connect failed", "error", err)
			s.reconnects.Add(1)

			delay := applyJitter(reconnectDelays[minInt(retryIndex, len(reconnectDelays)-1)], s.jitterRatio)
			if !sleepWithContext(ctx, delay) {
				return nil
			}
			if retryIndex < len(reconnectDelays)-1 {
				retryIndex++
			}
			continue
		}

		retryIndex = 0
		s.logger.Info("realtime socket.io connected", "url", s.baseURL)

		err = s.runConnection(ctx, client)
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}

		s.logger.Warn("realtime socket.io connection closed", "error", err)
		s.reconnects.Add(1)

		delay := applyJitter(reconnectDelays[minInt(retryIndex, len(reconnectDelays)-1)], s.jitterRatio)
		if !sleepWithContext(ctx, delay) {
			return nil
		}
		if retryIndex < len(reconnectDelays)-1 {
			retryIndex++
		}
	}
}

func (s *Service) TaskAccepted(ctx context.Context, taskID string, timestamp time.Time) error {
	err := s.enqueueCritical(ctx, taskAcceptedEvent{
		Type:      EventTaskAccepted,
		TaskID:    taskID,
		Timestamp: formatTimestampUTCMillis(timestamp),
	})
	if err == nil {
		s.lifecycleSent.Add(1)
	}
	return err
}

func (s *Service) TaskStarted(ctx context.Context, taskID string, timestamp time.Time) error {
	err := s.enqueueCritical(ctx, taskStartedEvent{
		Type:      EventTaskStarted,
		TaskID:    taskID,
		Timestamp: formatTimestampUTCMillis(timestamp),
	})
	if err == nil {
		s.lifecycleSent.Add(1)
	}
	return err
}

func (s *Service) TaskLog(ctx context.Context, taskID, stream, line string, timestamp time.Time) error {
	err := s.enqueueBestEffort(taskLogEvent{
		Type:      EventTaskLog,
		TaskID:    taskID,
		Stream:    stream,
		Line:      line,
		Timestamp: formatTimestampUTCMillis(timestamp),
	})
	if err == nil {
		s.lifecycleSent.Add(1)
	}
	return err
}

func (s *Service) TaskCompleted(ctx context.Context, event api.CompleteTaskRequest) error {
	err := s.enqueueCritical(ctx, taskCompletedEvent{
		Type:       EventTaskComplete,
		TaskID:     event.TaskID,
		Status:     event.Status,
		ExitCode:   event.ExitCode,
		Error:      event.Error,
		DurationMS: event.DurationMS,
		Timestamp:  formatTimestampUTCMillis(event.CompletedAt),
	})
	if err == nil {
		s.lifecycleSent.Add(1)
	}
	return err
}

func (s *Service) SendMetrics(ctx context.Context, event api.MetricsRequest) error {
	err := s.enqueueCritical(ctx, metricsEvent{
		Type:       EventAgentMetrics,
		NodeID:     event.NodeID,
		AgentToken: event.AgentToken,
		Timestamp:  formatTimestampUTCMillis(event.CollectedAt),
		CPU:        event.CPU,
		Memory:     event.Memory,
		Disk:       event.Disk,
		Networks:   event.Networks,
	})
	if err == nil {
		s.metricsSent.Add(1)
	}
	return err
}

func (s *Service) ReportLogDrop() {
	s.logDrops.Add(1)
}

func (s *Service) ReportDispatchHandled() {
	s.dispatchHandled.Add(1)
}

func (s *Service) SnapshotStats() Stats {
	return Stats{
		Reconnects:      s.reconnects.Load(),
		PingsSent:       s.pingsSent.Load(),
		MetricsSent:     s.metricsSent.Load(),
		LifecycleSent:   s.lifecycleSent.Load(),
		QueueDrops:      s.queueDrops.Load(),
		LogDrops:        s.logDrops.Load(),
		QueueDepth:      s.queueDepth.Load(),
		MaxQueueDepth:   s.maxQueueDepth.Load(),
		DispatchHandled: s.dispatchHandled.Load(),
	}
}

func (s *Service) connect(ctx context.Context) (*socketIOConn, error) {
	nodeID, agentToken := s.credentials()
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(agentToken) == "" {
		return nil, fmt.Errorf("agent credentials are missing")
	}

	reqHeader := transport.NewRequestHeader(nil)
	reqHeader.Set("Authorization", "Bearer "+agentToken)

	randomization := float32(s.jitterRatio)
	manager := sio.NewManager(s.baseURL, &sio.ManagerConfig{
		NoReconnection:      true,
		RandomizationFactor: &randomization,
		EIO: eio.ClientConfig{
			Transports:    []string{"websocket"},
			RequestHeader: reqHeader,
		},
	})
	socket := manager.Socket("/", &sio.ClientSocketConfig{})

	connectedCh := make(chan struct{}, 1)
	connectErrCh := make(chan error, 1)
	disconnectCh := make(chan error, 1)

	dispatchEvent := func(payload any) {
		envelope := inboundEnvelope{Type: EventTaskDispatch}
		if payload == nil {
			return
		}

		bytes, err := json.Marshal(payload)
		if err != nil {
			s.logger.Warn("failed to encode socket.io task payload", "error", err)
			return
		}

		var maybeEnvelope inboundEnvelope
		if err := json.Unmarshal(bytes, &maybeEnvelope); err == nil && maybeEnvelope.Task != nil {
			envelope = maybeEnvelope
		} else {
			var task api.Task
			if err := json.Unmarshal(bytes, &task); err != nil {
				s.logger.Warn("failed to decode socket.io task payload", "error", err)
				return
			}
			envelope.Task = &task
		}

		finalBytes, err := json.Marshal(envelope)
		if err != nil {
			s.logger.Warn("failed to marshal normalized dispatch envelope", "error", err)
			return
		}

		if err := s.dispatcher.handleMessage(ctx, finalBytes); err != nil {
			s.logger.Warn("realtime dispatch handling failed", "error", err)
		}
	}

	socket.OnConnect(func() {
		authPayload := authEvent{Type: EventAgentAuth, NodeID: nodeID, AgentToken: agentToken}
		socket.Emit(EventAgentAuth, authPayload)
		s.onAuthSuccess(ctx)
		select {
		case connectedCh <- struct{}{}:
		default:
		}
	})
	socket.OnConnectError(func(err any) {
		select {
		case connectErrCh <- fmt.Errorf("socket.io connect error: %v", err):
		default:
		}
	})
	socket.OnDisconnect(func(reason sio.Reason) {
		select {
		case disconnectCh <- fmt.Errorf("socket.io disconnected: %s", reason):
		default:
		}
	})
	manager.OnError(func(err error) {
		select {
		case connectErrCh <- fmt.Errorf("socket.io manager error: %w", err):
		default:
		}
	})

	socket.OnEvent(EventTaskDispatch, func(payload map[string]any) {
		dispatchEvent(payload)
	})
	socket.OnEvent("message", func(payload map[string]any) {
		dispatchEvent(payload)
	})

	socket.Connect()

	connectTimeout := s.requestTimeout
	if connectTimeout <= 0 {
		connectTimeout = 10 * time.Second
	}
	timeout := time.NewTimer(connectTimeout)
	defer timeout.Stop()

	select {
	case <-ctx.Done():
		manager.Close()
		return nil, ctx.Err()
	case <-timeout.C:
		manager.Close()
		return nil, fmt.Errorf("dial socket.io failed: connect timeout after %s", connectTimeout)
	case err := <-connectErrCh:
		manager.Close()
		return nil, fmt.Errorf("dial socket.io failed: %w", err)
	case <-connectedCh:
		return &socketIOConn{manager: manager, socket: socket, disconnectCh: disconnectCh}, nil
	}
}

func (s *Service) runConnection(ctx context.Context, conn *socketIOConn) error {
	defer conn.manager.Close()

	errCh := make(chan error, 1)
	go s.writeLoop(ctx, conn.socket, errCh)

	select {
	case <-ctx.Done():
		return nil
	case err := <-conn.disconnectCh:
		return err
	case err := <-errCh:
		return err
	}
}

func (s *Service) writeLoop(ctx context.Context, socket sio.ClientSocket, errCh chan<- error) {
	ticker := time.NewTicker(s.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case message := <-s.outbound:
			s.updateQueueDepth(-1)
			eventName, payload, err := eventForEmit(message)
			if err != nil {
				errCh <- err
				return
			}
			socket.Emit(eventName, payload)
		case <-ticker.C:
			payload := pingEvent{Type: EventAgentPing, Timestamp: formatTimestampUTCMillis(time.Now())}
			socket.Emit(EventAgentPing, payload)
			s.pingsSent.Add(1)
		}
	}
}

func eventForEmit(message any) (string, any, error) {
	data, err := json.Marshal(message)
	if err != nil {
		return "", nil, fmt.Errorf("marshal outbound event: %w", err)
	}

	payload := make(map[string]any)
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", nil, fmt.Errorf("decode outbound event map: %w", err)
	}

	eventValue, ok := payload["type"]
	if !ok {
		return "", nil, fmt.Errorf("outbound event missing type")
	}
	eventName, ok := eventValue.(string)
	if !ok || strings.TrimSpace(eventName) == "" {
		return "", nil, fmt.Errorf("outbound event type is invalid")
	}

	delete(payload, "type")
	if len(payload) == 0 {
		return eventName, map[string]any{"type": eventName}, nil
	}
	payload["type"] = eventName
	return eventName, payload, nil
}

func (s *Service) enqueueCritical(ctx context.Context, message any) error {
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.outbound <- message:
		s.updateQueueDepth(1)
		return nil
	}
}

func (s *Service) enqueueBestEffort(message any) error {
	select {
	case s.outbound <- message:
		s.updateQueueDepth(1)
		return nil
	default:
		s.queueDrops.Add(1)
		return fmt.Errorf("realtime outbound queue is full")
	}
}

func (s *Service) updateQueueDepth(delta int64) {
	depth := s.queueDepth.Add(delta)
	for {
		max := s.maxQueueDepth.Load()
		if depth <= max {
			return
		}
		if s.maxQueueDepth.CompareAndSwap(max, depth) {
			return
		}
	}
}

func realtimeEndpoint(apiURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil {
		return "", fmt.Errorf("parse API_URL for realtime endpoint: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("API_URL has unsupported scheme for socket.io endpoint: %q", parsed.Scheme)
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/agent-realtime"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func applyJitter(base time.Duration, ratio float64) time.Duration {
	if base <= 0 || ratio <= 0 {
		return base
	}
	maxJitter := int64(float64(base) * ratio)
	if maxJitter <= 0 {
		return base
	}
	offset := rand.Int63n((maxJitter*2)+1) - maxJitter
	jittered := int64(base) + offset
	if jittered < int64(time.Millisecond) {
		jittered = int64(time.Millisecond)
	}
	return time.Duration(jittered)
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
