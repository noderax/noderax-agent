package realtime

import (
	"context"
	"regexp"
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

	svc := &Service{outbound: make(chan any, 1)}
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
