package realtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

func TestApplyJitterWithinBounds(t *testing.T) {
	t.Parallel()

	base := 10 * time.Second
	ratio := 0.2
	min := time.Duration(float64(base) * (1 - ratio))
	max := time.Duration(float64(base) * (1 + ratio))

	for i := 0; i < 1000; i++ {
		value := applyJitter(base, ratio)
		if value < min || value > max {
			t.Fatalf("jittered delay out of bounds: got=%s min=%s max=%s", value, min, max)
		}
	}
}

func TestEnqueueBestEffortDropsWhenFull(t *testing.T) {
	t.Parallel()

	svc := &Service{outbound: make(chan any, 1)}
	if err := svc.enqueueBestEffort("first"); err != nil {
		t.Fatalf("first enqueue should succeed: %v", err)
	}
	if err := svc.enqueueBestEffort("second"); err == nil {
		t.Fatalf("second enqueue should fail when queue is full")
	}

	stats := svc.SnapshotStats()
	if stats.QueueDrops != 1 {
		t.Fatalf("queue drops mismatch: got=%d want=1", stats.QueueDrops)
	}
	if stats.QueueDepth != 1 {
		t.Fatalf("queue depth mismatch: got=%d want=1", stats.QueueDepth)
	}
	if stats.MaxQueueDepth != 1 {
		t.Fatalf("max queue depth mismatch: got=%d want=1", stats.MaxQueueDepth)
	}
}

func TestSendMetricsUsesMillisecondTimestamp(t *testing.T) {
	t.Parallel()

	svc := &Service{outbound: make(chan any, 1), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	svc.sessionActive.Store(true)
	svc.SetRuntimeAgentVersion("1.0.0")
	collectedAt := time.Date(2026, 3, 20, 10, 20, 30, 456789123, time.UTC)

	err := svc.SendMetrics(context.Background(), api.MetricsRequest{
		NodeID:      "node-1",
		AgentToken:  "token-1",
		CollectedAt: collectedAt,
	})
	if err != nil {
		t.Fatalf("SendMetrics returned error: %v", err)
	}

	msg := <-svc.outbound
	event, ok := msg.(metricsEvent)
	if !ok {
		t.Fatalf("unexpected event type: %T", msg)
	}
	if event.Type != EventAgentMetrics {
		t.Fatalf("unexpected event type: %q", event.Type)
	}
	if event.NodeID != "node-1" {
		t.Fatalf("unexpected node ID: %q", event.NodeID)
	}
	if event.AgentToken != "token-1" {
		t.Fatalf("unexpected agent token: %q", event.AgentToken)
	}
	if event.AgentVersion != "1.0.0" {
		t.Fatalf("unexpected agent version: %q", event.AgentVersion)
	}

	pattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	if !pattern.MatchString(event.Timestamp) {
		t.Fatalf("timestamp is not RFC3339 UTC milliseconds: %q", event.Timestamp)
	}

	stats := svc.SnapshotStats()
	if stats.MetricsSent != 1 {
		t.Fatalf("metrics sent counter mismatch: got=%d want=1", stats.MetricsSent)
	}
}

func TestReportLogDropIncrementsCounter(t *testing.T) {
	t.Parallel()

	svc := &Service{}
	svc.ReportLogDrop()
	svc.ReportLogDrop()

	if got := svc.SnapshotStats().LogDrops; got != 2 {
		t.Fatalf("log drop counter mismatch: got=%d want=2", got)
	}
}

func TestNormalizeRealtimeTargetDefaults(t *testing.T) {
	t.Parallel()

	target, err := normalizeRealtimeTarget("api.example.com", "", "")
	if err != nil {
		t.Fatalf("normalizeRealtimeTarget() error = %v", err)
	}

	if target.DialURL != "https://api.example.com/socket.io/" {
		t.Fatalf("unexpected dial URL: %q", target.DialURL)
	}
	if target.Namespace != "/agent-realtime" {
		t.Fatalf("unexpected namespace: %q", target.Namespace)
	}
	if target.Path != "/socket.io/" {
		t.Fatalf("unexpected path: %q", target.Path)
	}
}

func TestNormalizeRealtimeTargetRejectsInvalidURL(t *testing.T) {
	t.Parallel()

	if _, err := normalizeRealtimeTarget("http://", "/agent-realtime", "/socket.io/"); err == nil {
		t.Fatalf("expected invalid URL error")
	}
}

func TestEventForEmitRetainsType(t *testing.T) {
	t.Parallel()

	eventName, payload, err := eventForEmit(taskAcceptedEvent{
		Type:      EventTaskAccepted,
		TaskID:    "task-1",
		Timestamp: "2026-03-20T22:00:00.123Z",
	})
	if err != nil {
		t.Fatalf("eventForEmit() error = %v", err)
	}
	if eventName != EventTaskAccepted {
		t.Fatalf("unexpected event name: %q", eventName)
	}

	serialized, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	jsonText := string(serialized)
	if !regexp.MustCompile(`"type":"task.accepted"`).MatchString(jsonText) {
		t.Fatalf("payload is missing type field: %s", jsonText)
	}
}

func TestSanitizeUsage(t *testing.T) {
	t.Parallel()

	if value := sanitizeUsage(-1); value == nil || *value != 0 {
		t.Fatalf("expected floor to 0, got %#v", value)
	}
	if value := sanitizeUsage(101); value == nil || *value != 100 {
		t.Fatalf("expected cap to 100, got %#v", value)
	}
}

func TestTaskAcceptedOmitsAuthFields(t *testing.T) {
	t.Parallel()

	svc := &Service{
		outbound: make(chan any, 1),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.TaskAccepted(context.Background(), "task-1", time.Now().UTC()); err != nil {
		t.Fatalf("TaskAccepted() error = %v", err)
	}

	msg := <-svc.outbound
	bytes, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal accepted event: %v", err)
	}
	text := string(bytes)
	if strings.Contains(text, "nodeId") || strings.Contains(text, "agentToken") {
		t.Fatalf("task.accepted must not include auth fields, payload=%s", text)
	}
}

func TestTaskCompletedTruncatesOutputAndOmitsAuthFields(t *testing.T) {
	t.Parallel()

	svc := &Service{
		outbound: make(chan any, 1),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	veryLargeOutput := strings.Repeat("x", maxTaskOutputChars+500)
	err := svc.TaskCompleted(context.Background(), api.CompleteTaskRequest{
		TaskID:      "task-1",
		Status:      "success",
		Output:      veryLargeOutput,
		ExitCode:    0,
		CompletedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("TaskCompleted() error = %v", err)
	}

	msg := <-svc.outbound
	event, ok := msg.(taskCompletedEvent)
	if !ok {
		t.Fatalf("unexpected event type: %T", msg)
	}
	if len([]rune(event.Output)) > maxTaskOutputChars {
		t.Fatalf("task.completed output exceeds max chars: got=%d max=%d", len([]rune(event.Output)), maxTaskOutputChars)
	}
	if !strings.Contains(event.Output, "[truncated") {
		t.Fatalf("expected truncation marker in output")
	}

	bytes, marshalErr := json.Marshal(event)
	if marshalErr != nil {
		t.Fatalf("marshal completed event: %v", marshalErr)
	}
	text := string(bytes)
	if strings.Contains(text, "nodeId") || strings.Contains(text, "agentToken") {
		t.Fatalf("task.completed must not include auth fields, payload=%s", text)
	}
}

func TestTaskStartedOmitsAuthFields(t *testing.T) {
	t.Parallel()

	svc := &Service{
		outbound: make(chan any, 1),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.TaskStarted(context.Background(), "task-1", time.Now().UTC()); err != nil {
		t.Fatalf("TaskStarted() error = %v", err)
	}

	msg := <-svc.outbound
	bytes, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal started event: %v", err)
	}
	text := string(bytes)
	if strings.Contains(text, "nodeId") || strings.Contains(text, "agentToken") {
		t.Fatalf("task.started must not include auth fields, payload=%s", text)
	}
}

func TestTaskLogOmitsAuthFields(t *testing.T) {
	t.Parallel()

	svc := &Service{
		outbound: make(chan any, 1),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.TaskLog(context.Background(), "task-1", "stdout", "hello", time.Now().UTC()); err != nil {
		t.Fatalf("TaskLog() error = %v", err)
	}

	msg := <-svc.outbound
	bytes, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal log event: %v", err)
	}
	text := string(bytes)
	if strings.Contains(text, "nodeId") || strings.Contains(text, "agentToken") {
		t.Fatalf("task.log must not include auth fields, payload=%s", text)
	}
}
