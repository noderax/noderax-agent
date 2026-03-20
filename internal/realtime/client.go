package realtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/noderax/noderax-agent/internal/api"
)

var reconnectDelays = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}

type Service struct {
	logger         *slog.Logger
	requestTimeout time.Duration
	pingInterval   time.Duration
	credentials    func() (string, string)
	dispatcher     *dispatcher

	dialer   websocket.Dialer
	wsURL    string
	outbound chan any

	mu   sync.RWMutex
	conn *websocket.Conn
}

func NewService(
	apiURL string,
	requestTimeout time.Duration,
	pingInterval time.Duration,
	logger *slog.Logger,
	credentials func() (string, string),
	handler taskDispatcher,
) (*Service, error) {
	wsURL, err := realtimeEndpoint(apiURL)
	if err != nil {
		return nil, err
	}

	return &Service{
		logger:         logger,
		requestTimeout: requestTimeout,
		pingInterval:   pingInterval,
		credentials:    credentials,
		dispatcher:     newDispatcher(handler),
		dialer: websocket.Dialer{
			HandshakeTimeout: requestTimeout,
		},
		wsURL:    wsURL,
		outbound: make(chan any, 1024),
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
			if !sleepWithContext(ctx, reconnectDelays[minInt(retryIndex, len(reconnectDelays)-1)]) {
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
		if !sleepWithContext(ctx, reconnectDelays[minInt(retryIndex, len(reconnectDelays)-1)]) {
			return nil
		}
		if retryIndex < len(reconnectDelays)-1 {
			retryIndex++
		}
	}
}

func (s *Service) TaskAccepted(ctx context.Context, taskID string, timestamp time.Time) error {
	return s.enqueue(ctx, taskAcceptedEvent{
		Type:      EventTaskAccepted,
		TaskID:    taskID,
		Timestamp: timestamp.UTC(),
	})
}

func (s *Service) TaskStarted(ctx context.Context, taskID string, timestamp time.Time) error {
	return s.enqueue(ctx, taskStartedEvent{
		Type:      EventTaskStarted,
		TaskID:    taskID,
		Timestamp: timestamp.UTC(),
	})
}

func (s *Service) TaskLog(ctx context.Context, taskID, stream, line string, timestamp time.Time) error {
	return s.enqueue(ctx, taskLogEvent{
		Type:      EventTaskLog,
		TaskID:    taskID,
		Stream:    stream,
		Line:      line,
		Timestamp: timestamp.UTC(),
	})
}

func (s *Service) TaskCompleted(ctx context.Context, event api.CompleteTaskRequest) error {
	return s.enqueue(ctx, taskCompletedEvent{
		Type:       EventTaskComplete,
		TaskID:     event.TaskID,
		Status:     event.Status,
		ExitCode:   event.ExitCode,
		Error:      event.Error,
		DurationMS: event.DurationMS,
		Timestamp:  event.CompletedAt.UTC(),
	})
}

func (s *Service) SendMetrics(ctx context.Context, event api.MetricsRequest) error {
	return s.enqueue(ctx, metricsEvent{
		Type:       EventAgentMetrics,
		NodeID:     event.NodeID,
		AgentToken: event.AgentToken,
		Timestamp:  event.CollectedAt.UTC(),
		CPU:        event.CPU,
		Memory:     event.Memory,
		Disk:       event.Disk,
		Networks:   event.Networks,
	})
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
			if err := writeJSON(ctx, conn, message); err != nil {
				errCh <- err
				return
			}
		case <-ticker.C:
			if err := writeJSON(ctx, conn, pingEvent{Type: EventAgentPing, Timestamp: time.Now().UTC()}); err != nil {
				errCh <- err
				return
			}
		}
	}
}

func (s *Service) enqueue(ctx context.Context, message any) error {
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case s.outbound <- message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("realtime outbound queue is full")
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
