package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	sio "github.com/karagenc/socket.io-go"
	eio "github.com/karagenc/socket.io-go/engine.io"
	"github.com/karagenc/socket.io-go/engine.io/transport"
	"github.com/noderax/noderax-agent/internal/api"
)

const (
	defaultRealtimeNamespace = "/agent-realtime"
	defaultRealtimePath      = "/socket.io/"
	maxTaskOutputChars       = 8000
)

type AuthSuccessHook func(context.Context)

var ErrSessionNotActive = errors.New("realtime session is not authenticated")

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
	dialURL        string
	healthURL      string
	namespace      string
	path           string
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
	selfChecked     atomic.Bool
	sessionActive   atomic.Bool
}

type socketIOConn struct {
	manager      *sio.Manager
	socket       sio.ClientSocket
	disconnectCh <-chan error
}

func NewService(
	apiURL string,
	namespace string,
	path string,
	requestTimeout time.Duration,
	pingInterval time.Duration,
	queueSize int,
	jitterRatio float64,
	logger *slog.Logger,
	credentials func() (string, string),
	handler taskDispatcher,
	onAuthSuccess AuthSuccessHook,
) (*Service, error) {
	target, err := normalizeRealtimeTarget(apiURL, namespace, path)
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
	if pingInterval > 2*time.Second {
		logger.Warn("realtime ping interval may be too high and can trigger server disconnect", "ping_interval", pingInterval, "recommended_max", 2*time.Second)
	}
	if onAuthSuccess == nil {
		onAuthSuccess = func(context.Context) {}
	}

	svc := &Service{
		logger:         logger,
		requestTimeout: requestTimeout,
		pingInterval:   pingInterval,
		jitterRatio:    jitterRatio,
		credentials:    credentials,
		dispatcher:     newDispatcher(handler),
		onAuthSuccess:  onAuthSuccess,
		dialURL:        target.DialURL,
		healthURL:      target.HealthURL,
		namespace:      target.Namespace,
		path:           target.Path,
		outbound:       make(chan any, queueSize),
	}
	return svc, nil
}

func (s *Service) Run(ctx context.Context) error {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		if !s.selfChecked.Load() {
			if err := s.preflightCheck(ctx); err != nil {
				s.logger.Warn("realtime startup self-check failed", "error", err, "health_url", s.healthURL)
			} else {
				s.logger.Info("realtime startup self-check passed", "health_url", s.healthURL)
			}
			s.selfChecked.Store(true)
		}

		client, err := s.connect(ctx)
		if err != nil {
			s.logger.Warn("realtime connect failed", "error", err)
			s.reconnects.Add(1)

			delay := backoffDelay(attempt, s.jitterRatio)
			if !sleepWithContext(ctx, delay) {
				return nil
			}
			attempt++
			continue
		}

		attempt = 0
		s.logger.Info("realtime socket.io connected", "url", s.dialURL, "namespace", s.namespace, "path", s.path)

		err = s.runConnection(ctx, client)
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}

		s.logger.Warn("realtime socket.io connection closed", "error", err)
		s.reconnects.Add(1)

		delay := backoffDelay(attempt, s.jitterRatio)
		if !sleepWithContext(ctx, delay) {
			return nil
		}
		attempt++
	}
}

func (s *Service) TaskAccepted(ctx context.Context, taskID string, timestamp time.Time) error {
	s.logger.Debug("emitting lifecycle event", "event", EventTaskAccepted, "task_id", taskID, "payload_keys", "type,taskId,timestamp")
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
	s.logger.Debug("emitting lifecycle event", "event", EventTaskStarted, "task_id", taskID, "payload_keys", "type,taskId,timestamp")
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
	s.logger.Debug("emitting lifecycle event", "event", EventTaskLog, "task_id", taskID, "payload_keys", "type,taskId,stream,line,timestamp")
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
	boundedOutput, truncated := truncateTaskOutput(event.Output, maxTaskOutputChars)
	outputLength := len([]rune(boundedOutput))
	s.logger.Debug(
		"emitting lifecycle event",
		"event", EventTaskComplete,
		"task_id", event.TaskID,
		"payload_keys", "type,taskId,status,result,output,exitCode,error,timestamp,durationMs",
		"output_truncated", truncated,
		"output_length", outputLength,
	)

	err := s.enqueueCritical(ctx, taskCompletedEvent{
		Type:       EventTaskComplete,
		TaskID:     event.TaskID,
		Status:     event.Status,
		Result:     event.Result,
		Output:     boundedOutput,
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
	if !s.sessionActive.Load() {
		return ErrSessionNotActive
	}

	cpuUsage := sanitizeUsageOrZero(event.CPU.UsagePercent)
	memoryUsage := sanitizeUsageOrZero(event.Memory.UsedPercent)
	diskUsage := sanitizeUsageOrZero(event.Disk.UsedPercent)
	networks := event.Networks
	if networks == nil {
		networks = []api.NetworkStats{}
	}

	s.logger.Debug("preparing realtime metrics payload",
		"networkStats_present", len(networks) > 0,
		"networkStats_count", len(networks),
		"keys", "type,nodeId,agentToken,timestamp,cpuUsage,memoryUsage,diskUsage,networkStats,networks",
	)

	err := s.enqueueCritical(ctx, metricsEvent{
		Type:         EventAgentMetrics,
		NodeID:       event.NodeID,
		AgentToken:   event.AgentToken,
		Timestamp:    formatTimestampUTCMillis(event.CollectedAt),
		CPUUsage:     &cpuUsage,
		MemoryUsage:  &memoryUsage,
		DiskUsage:    &diskUsage,
		NetworkStats: networks,
		Networks:     networks,
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

	randomization := float32(s.jitterRatio)
	transportMode := "websocket-only"
	transports := []string{"websocket"}
	s.logger.Info("realtime dial attempt", "base_url", s.dialURL, "namespace", s.namespace, "path", s.path, "transport_mode", transportMode)

	reqHeader := transport.NewRequestHeader(http.Header{})
	manager := sio.NewManager(s.dialURL, &sio.ManagerConfig{
		NoReconnection:      true,
		RandomizationFactor: &randomization,
		EIO: eio.ClientConfig{
			Transports:    transports, // Forcing websocket only
			RequestHeader: reqHeader,
		},
	})
	socket := manager.Socket(s.namespace, &sio.ClientSocketConfig{})

	authOKCh := make(chan struct{}, 1)
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

		s.logger.Info("raw socket.io dispatch payload received", "raw_json", string(bytes))

		var maybeEnvelope inboundEnvelope
		if err := json.Unmarshal(bytes, &maybeEnvelope); err == nil && maybeEnvelope.Task != nil && (maybeEnvelope.Task.ID != "" || maybeEnvelope.Task.Type != "") {
			envelope = maybeEnvelope
		} else {
			// It might be a direct Task object, possibly using alternative field names
			// We'll normalize it using a generic map first
			var rawMap map[string]any
			if err := json.Unmarshal(bytes, &rawMap); err == nil {
				if id, ok := rawMap["taskId"].(string); ok && id != "" {
					rawMap["id"] = id
				}
				if action, ok := rawMap["action"].(string); ok && action != "" {
					rawMap["type"] = action
				}
				if data, ok := rawMap["data"]; ok {
					rawMap["payload"] = data
				}
				if timeoutVal, ok := rawMap["timeout"]; ok {
					if tFloat, ok := timeoutVal.(float64); ok {
						rawMap["timeoutSeconds"] = int(tFloat)
					}
				}
				if modifiedBytes, err := json.Marshal(rawMap); err == nil {
					bytes = modifiedBytes
				}
			}

			var task api.Task
			if err := json.Unmarshal(bytes, &task); err != nil {
				s.logger.Warn("failed to decode socket.io task payload", "error", err)
				return
			}
			if task.ID == "" {
				s.logger.Warn("decoded task is missing ID", "raw_json", string(bytes))
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
	})

	socket.OnEvent(EventAgentAuthAck, func(payload map[string]any) {
		ack := authAckEvent{}
		if bytes, err := json.Marshal(payload); err == nil {
			_ = json.Unmarshal(bytes, &ack)
		}
		if !ack.Authenticated {
			select {
			case connectErrCh <- fmt.Errorf("auth error: server returned authenticated=false"):
			default:
			}
			return
		}
		s.logger.Info("realtime auth acknowledged", "node_id", ack.NodeID)
		s.sessionActive.Store(true)
		s.onAuthSuccess(ctx)
		select {
		case authOKCh <- struct{}{}:
		default:
		}
	})

	socket.OnEvent(EventAgentAuthErr, func(payload map[string]any) {
		authErr := authErrorEvent{}
		if bytes, err := json.Marshal(payload); err == nil {
			_ = json.Unmarshal(bytes, &authErr)
		}
		msg := strings.TrimSpace(authErr.Message)
		if msg == "" {
			msg = strings.TrimSpace(authErr.Error)
		}
		if msg == "" {
			msg = "unknown auth error"
		}
		s.sessionActive.Store(false)
		select {
		case connectErrCh <- fmt.Errorf("auth error: %s", msg):
		default:
		}
	})

	socket.OnEvent(EventAgentError, func(payload map[string]any) {
		s.logger.Warn("received realtime agent.error", "payload", payload)
	})

	socket.OnConnectError(func(err any) {
		s.logger.Warn("realtime namespace connect error", "error", err, "url", s.dialURL, "namespace", s.namespace, "path", s.path)
		select {
		case connectErrCh <- fmt.Errorf("namespace connect failure: %v", err):
		default:
		}
	})
	socket.OnDisconnect(func(reason sio.Reason) {
		s.sessionActive.Store(false)
		s.logger.Warn("realtime socket disconnected", "reason", reason, "url", s.dialURL, "namespace", s.namespace, "path", s.path)
		select {
		case disconnectCh <- fmt.Errorf("socket.io disconnected: %s", reason):
		default:
		}
	})
	manager.OnError(func(err error) {
		s.sessionActive.Store(false)
		s.logger.Warn("realtime manager error", "error", err, "url", s.dialURL, "namespace", s.namespace, "path", s.path)
		select {
		case connectErrCh <- classifyDialError(err):
		default:
		}
	})

	socket.OnEvent(EventTaskDispatch, func(payload any, extra ...any) {
		if len(extra) > 0 {
			s.logger.Info("realtime multi-arg dispatch received", "args_count", 1+len(extra))
			
			// Check if any extra arg is the actual task
			for _, arg := range extra {
				if argMap, ok := arg.(map[string]any); ok {
					if _, hasID := argMap["id"]; hasID {
						dispatchEvent(arg)
						return
					}
					if _, hasTaskID := argMap["taskId"]; hasTaskID {
						dispatchEvent(arg)
						return
					}
					if _, hasTask := argMap["task"]; hasTask {
						dispatchEvent(arg)
						return
					}
				}
			}
		}
		
		dispatchEvent(payload)
	})
	socket.OnEvent("message", func(payload any) {
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
		return nil, fmt.Errorf("dial socket.io failed: namespace auth timeout after %s", connectTimeout)
	case err := <-connectErrCh:
		manager.Close()
		return nil, fmt.Errorf("dial socket.io failed: %w", err)
	case <-authOKCh:
		return &socketIOConn{manager: manager, socket: socket, disconnectCh: disconnectCh}, nil
	}
}

func (s *Service) runConnection(ctx context.Context, conn *socketIOConn) error {
	defer conn.manager.Close()

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)
	go s.writeLoop(subCtx, conn.socket, errCh)

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

type realtimeTarget struct {
	DialURL   string
	HealthURL string
	Namespace string
	Path      string
}

func normalizeRealtimeTarget(apiURL, namespace, path string) (realtimeTarget, error) {
	trimmed := strings.TrimSpace(apiURL)
	if trimmed == "" {
		return realtimeTarget{}, fmt.Errorf("API_URL is required")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return realtimeTarget{}, fmt.Errorf("invalid URL input: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return realtimeTarget{}, fmt.Errorf("invalid URL input: unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return realtimeTarget{}, fmt.Errorf("invalid URL input: missing host")
	}

	if strings.TrimSpace(namespace) == "" {
		namespace = defaultRealtimeNamespace
	}
	if !strings.HasPrefix(namespace, "/") {
		namespace = "/" + namespace
	}

	if strings.TrimSpace(path) == "" {
		path = defaultRealtimePath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}

	base := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	dial := *base
	dial.Path = path

	health := *base
	health.Path = path
	q := health.Query()
	q.Set("EIO", "4")
	q.Set("transport", "polling") // Keep polling for health-check purely because it's a simple HTTP GET
	health.RawQuery = q.Encode()

	return realtimeTarget{
		DialURL:   dial.String(),
		HealthURL: health.String(),
		Namespace: namespace,
		Path:      path,
	}, nil
}

func (s *Service) preflightCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.healthURL, nil)
	if err != nil {
		return fmt.Errorf("build self-check request: %w", err)
	}

	client := &http.Client{Timeout: s.requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("self-check request failed: %w", classifyDialError(err))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	text := string(body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(text, "\"sid\"") {
		return fmt.Errorf("self-check failed: status=%d body=%q (expected Socket.IO handshake with sid)", resp.StatusCode, text)
	}
	return nil
}

func classifyDialError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "invalid input") || strings.Contains(message, "invalid url"):
		return fmt.Errorf("invalid URL input: %w", err)
	case strings.Contains(message, "tls") || strings.Contains(message, "certificate") || strings.Contains(message, "x509"):
		return fmt.Errorf("tls/proxy handshake failure: %w", err)
	case strings.Contains(message, "connect error") || strings.Contains(message, "namespace"):
		return fmt.Errorf("namespace connect failure: %w", err)
	default:
		return fmt.Errorf("transport dial failure: %w", err)
	}
}

func sanitizeUsage(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	return &value
}

func sanitizeUsageOrZero(value float64) float64 {
	v := sanitizeUsage(value)
	if v == nil {
		return 0
	}
	return *v
}

func truncateTaskOutput(output string, maxChars int) (string, bool) {
	runes := []rune(output)
	if maxChars <= 0 || len(runes) <= maxChars {
		return output, false
	}

	removed := len(runes) - maxChars
	marker := fmt.Sprintf("\n...[truncated %d chars]...\n", removed)
	markerRunes := []rune(marker)
	if len(markerRunes) >= maxChars {
		return string(runes[:maxChars]), true
	}

	remaining := maxChars - len(markerRunes)
	head := remaining / 2
	tail := remaining - head

	truncated := string(runes[:head]) + marker + string(runes[len(runes)-tail:])
	return truncated, true
}

func backoffDelay(attempt int, jitterRatio float64) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := time.Second << minInt(attempt, 5)
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	return applyJitter(base, jitterRatio)
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
