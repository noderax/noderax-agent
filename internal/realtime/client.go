package realtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
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

	dialer   websocket.Dialer
	wsURL    string
	outbound chan any

	mu   sync.RWMutex
	conn *websocket.Conn

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
	wsURL, err := realtimeEndpoint(apiURL)
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
		dialer: websocket.Dialer{
			HandshakeTimeout: requestTimeout,
		},
		wsURL:    wsURL,
		outbound: make(chan any, queueSize),
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	defer s.closeConnection()

	retryIndex := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		conn, err := s.connect(ctx)
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
		s.logger.Info("realtime websocket connected", "url", s.wsURL)

		err = s.runConnection(ctx, conn)
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}

		s.logger.Warn("realtime connection closed", "error", err)
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

func (s *Service) connect(ctx context.Context) (*websocket.Conn, error) {
	nodeID, agentToken := s.credentials()
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(agentToken) == "" {
		return nil, fmt.Errorf("agent credentials are missing")
	}

	header := make(http.Header)
	header.Set("Authorization", "Bearer "+agentToken)

	conn, response, err := s.dialer.DialContext(ctx, s.wsURL, header)
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("dial websocket failed: status=%d: %w", response.StatusCode, err)
		}
		return nil, fmt.Errorf("dial websocket failed: %w", err)
	}

	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("clear websocket read deadline: %w", err)
	}

	authCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()
	if err := writeJSON(authCtx, conn, authEvent{Type: EventAgentAuth, NodeID: nodeID, AgentToken: agentToken}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send realtime auth event: %w", err)
	}

	s.onAuthSuccess(ctx)

	s.setConnection(conn)
	return conn, nil
}

func (s *Service) runConnection(ctx context.Context, conn *websocket.Conn) error {
	defer s.closeConnection()

	errCh := make(chan error, 2)
	go s.readLoop(ctx, conn, errCh)
	go s.writeLoop(ctx, conn, errCh)

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Service) readLoop(ctx context.Context, conn *websocket.Conn, errCh chan<- error) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}

		if err := s.dispatcher.handleMessage(ctx, data); err != nil {
			s.logger.Warn("realtime message handling failed", "error", err)
		}
	}
}

func (s *Service) writeLoop(ctx context.Context, conn *websocket.Conn, errCh chan<- error) {
	ticker := time.NewTicker(s.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case message := <-s.outbound:
			s.updateQueueDepth(-1)
			if err := writeJSON(ctx, conn, message); err != nil {
				errCh <- err
				return
			}
		case <-ticker.C:
			if err := writeJSON(ctx, conn, pingEvent{Type: EventAgentPing, Timestamp: formatTimestampUTCMillis(time.Now())}); err != nil {
				errCh <- err
				return
			}
			s.pingsSent.Add(1)
		}
	}
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

func (s *Service) setConnection(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = conn
}

func (s *Service) closeConnection() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return
	}
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
	_ = s.conn.Close()
	s.conn = nil
}

func realtimeEndpoint(apiURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil {
		return "", fmt.Errorf("parse API_URL for realtime endpoint: %w", err)
	}

	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("API_URL has unsupported scheme for realtime endpoint: %q", parsed.Scheme)
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/agent-realtime"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func writeJSON(ctx context.Context, conn *websocket.Conn, payload any) error {
	deadline := time.Now().Add(10 * time.Second)
	if value, ok := ctx.Deadline(); ok && !value.IsZero() {
		deadline = value
	}

	if err := conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set websocket write deadline: %w", err)
	}
	if err := conn.WriteJSON(payload); err != nil {
		return fmt.Errorf("write websocket message: %w", err)
	}
	return nil
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
