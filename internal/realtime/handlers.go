package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

const (
	EventAgentAuth    = "agent.auth"
	EventAgentPing    = "agent.ping"
	EventAgentMetrics = "agent.metrics"
	EventTaskDispatch = "task.dispatch"
	EventTaskAccepted = "task.accepted"
	EventTaskStarted  = "task.started"
	EventTaskLog      = "task.log"
	EventTaskComplete = "task.completed"
)

type taskDispatcher interface {
	DispatchRealtimeTask(context.Context, api.Task) bool
}

type dispatcher struct {
	handler taskDispatcher
}

func newDispatcher(handler taskDispatcher) *dispatcher {
	return &dispatcher{handler: handler}
}

type inboundEnvelope struct {
	Type string    `json:"type"`
	Task *api.Task `json:"task,omitempty"`
}

func (d *dispatcher) handleMessage(ctx context.Context, data []byte) error {
	var envelope inboundEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode inbound message: %w", err)
	}

	switch envelope.Type {
	case EventTaskDispatch:
		if envelope.Task == nil {
			return fmt.Errorf("task.dispatch is missing task payload")
		}

		d.handler.DispatchRealtimeTask(ctx, *envelope.Task)
		return nil
	default:
		return nil
	}
}

type authEvent struct {
	Type       string `json:"type"`
	NodeID     string `json:"nodeId"`
	AgentToken string `json:"agentToken"`
}

type pingEvent struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
}

type metricsEvent struct {
	Type       string             `json:"type"`
	NodeID     string             `json:"nodeId"`
	AgentToken string             `json:"agentToken"`
	Timestamp  time.Time          `json:"timestamp"`
	CPU        api.CPUStats       `json:"cpu"`
	Memory     api.MemoryStats    `json:"memory"`
	Disk       api.DiskStats      `json:"disk"`
	Networks   []api.NetworkStats `json:"networks"`
}

type taskAcceptedEvent struct {
	Type      string    `json:"type"`
	TaskID    string    `json:"taskId"`
	Timestamp time.Time `json:"timestamp"`
}

type taskStartedEvent struct {
	Type      string    `json:"type"`
	TaskID    string    `json:"taskId"`
	Timestamp time.Time `json:"timestamp"`
}

type taskLogEvent struct {
	Type      string    `json:"type"`
	TaskID    string    `json:"taskId"`
	Stream    string    `json:"stream"`
	Line      string    `json:"line"`
	Timestamp time.Time `json:"timestamp"`
}

type taskCompletedEvent struct {
	Type       string    `json:"type"`
	TaskID     string    `json:"taskId"`
	Status     string    `json:"status"`
	ExitCode   int       `json:"exitCode"`
	Error      string    `json:"error,omitempty"`
	DurationMS int64     `json:"durationMs"`
	Timestamp  time.Time `json:"timestamp"`
}
