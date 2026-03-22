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
	EventAgentAuthAck = "agent.auth.ack"
	EventAgentAuthErr = "agent.auth.error"
	EventAgentError   = "agent.error"
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
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

type metricsEvent struct {
	Type         string             `json:"type"`
	NodeID       string             `json:"nodeId"`
	AgentToken   string             `json:"agentToken"`
	Timestamp    string             `json:"timestamp"`
	CPUUsage     *float64           `json:"cpuUsage,omitempty"`
	MemoryUsage  *float64           `json:"memoryUsage,omitempty"`
	DiskUsage    *float64           `json:"diskUsage,omitempty"`
	NetworkStats []api.NetworkStats `json:"networkStats"`
	CPU          *api.CPUStats      `json:"cpu,omitempty"`
	Memory       *api.MemoryStats   `json:"memory,omitempty"`
	Disk         *api.DiskStats     `json:"disk,omitempty"`
	Networks     []api.NetworkStats `json:"networks,omitempty"`
}

type taskAcceptedEvent struct {
	Type       string `json:"type"`
	NodeID     string `json:"nodeId,omitempty"`
	AgentToken string `json:"agentToken,omitempty"`
	TaskID     string `json:"taskId"`
	Timestamp  string `json:"timestamp"`
}

type taskStartedEvent struct {
	Type       string `json:"type"`
	NodeID     string `json:"nodeId,omitempty"`
	AgentToken string `json:"agentToken,omitempty"`
	TaskID     string `json:"taskId"`
	Timestamp  string `json:"timestamp"`
}

type taskLogEvent struct {
	Type       string `json:"type"`
	NodeID     string `json:"nodeId,omitempty"`
	AgentToken string `json:"agentToken,omitempty"`
	TaskID     string `json:"taskId"`
	Stream     string `json:"stream"`
	Line       string `json:"line"`
	Timestamp  string `json:"timestamp"`
}

type taskCompletedEvent struct {
	Type       string `json:"type"`
	NodeID     string `json:"nodeId,omitempty"`
	AgentToken string `json:"agentToken,omitempty"`
	TaskID     string `json:"taskId"`
	Status     string `json:"status"`
	Result     any    `json:"result,omitempty"`
	Output     string `json:"output,omitempty"`
	ExitCode   int    `json:"exitCode"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"durationMs"`
	Timestamp  string `json:"timestamp"`
}

type authAckEvent struct {
	Authenticated bool   `json:"authenticated"`
	NodeID        string `json:"nodeId,omitempty"`
}

type authErrorEvent struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

func formatTimestampUTCMillis(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
