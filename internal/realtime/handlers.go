package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

const (
	EventAgentAuth         = "agent.auth"
	EventAgentAuthAck      = "agent.auth.ack"
	EventAgentAuthErr      = "agent.auth.error"
	EventAgentError        = "agent.error"
	EventAgentPing         = "agent.ping"
	EventAgentMetrics      = "agent.metrics"
	EventRootAccessUpdated = "root-access.updated"
	EventTerminalStart     = "terminal.start"
	EventTerminalInput     = "terminal.input"
	EventTerminalResize    = "terminal.resize"
	EventTerminalStop      = "terminal.stop"
	EventTerminalOpened    = "terminal.opened"
	EventTerminalOutput    = "terminal.output"
	EventTerminalExited    = "terminal.exited"
	EventTerminalError     = "terminal.error"
	EventTaskDispatch      = "task.dispatch"
	EventTaskAccepted      = "task.accepted"
	EventTaskStarted       = "task.started"
	EventTaskLog           = "task.log"
	EventTaskComplete      = "task.completed"
)

type taskDispatcher interface {
	DispatchRealtimeTask(context.Context, api.Task) bool
}

type terminalStartDispatcher interface {
	StartTerminalSession(context.Context, string, int, int, bool) error
}

type terminalInputDispatcher interface {
	WriteTerminalInput(context.Context, string, string) error
}

type terminalResizeDispatcher interface {
	ResizeTerminalSession(context.Context, string, int, int) error
}

type terminalStopDispatcher interface {
	StopTerminalSession(context.Context, string, string) error
}

type dispatcher struct {
	handler any
}

func newDispatcher(handler any) *dispatcher {
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

		taskHandler, ok := d.handler.(taskDispatcher)
		if !ok {
			return nil
		}
		taskHandler.DispatchRealtimeTask(ctx, *envelope.Task)
		return nil
	case EventTerminalStart:
		var event terminalStartEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode terminal.start payload: %w", err)
		}
		handler, ok := d.handler.(terminalStartDispatcher)
		if !ok {
			return nil
		}
		return handler.StartTerminalSession(
			ctx,
			event.SessionID,
			event.Cols,
			event.Rows,
			event.RunAsRoot,
		)
	case EventTerminalInput:
		var event terminalInputEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode terminal.input payload: %w", err)
		}
		handler, ok := d.handler.(terminalInputDispatcher)
		if !ok {
			return nil
		}
		return handler.WriteTerminalInput(ctx, event.SessionID, event.Payload)
	case EventTerminalResize:
		var event terminalResizeEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode terminal.resize payload: %w", err)
		}
		handler, ok := d.handler.(terminalResizeDispatcher)
		if !ok {
			return nil
		}
		return handler.ResizeTerminalSession(ctx, event.SessionID, event.Cols, event.Rows)
	case EventTerminalStop:
		var event terminalStopEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode terminal.stop payload: %w", err)
		}
		handler, ok := d.handler.(terminalStopDispatcher)
		if !ok {
			return nil
		}
		return handler.StopTerminalSession(ctx, event.SessionID, event.Reason)
	default:
		return nil
	}
}

type authEvent struct {
	Type            string                     `json:"type"`
	NodeID          string                     `json:"nodeId"`
	AgentToken      string                     `json:"agentToken"`
	AgentVersion    string                     `json:"agentVersion,omitempty"`
	PlatformVersion string                     `json:"platformVersion,omitempty"`
	KernelVersion   string                     `json:"kernelVersion,omitempty"`
	RootAccess      *api.RootAccessAgentReport `json:"rootAccess,omitempty"`
	Location        *api.NodeLocation          `json:"location,omitempty"`
}

type pingEvent struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

type metricsEvent struct {
	Type         string             `json:"type"`
	NodeID       string             `json:"nodeId"`
	AgentToken   string             `json:"agentToken"`
	AgentVersion string             `json:"agentVersion,omitempty"`
	Timestamp    string             `json:"timestamp"`
	CPUUsage     *float64           `json:"cpuUsage,omitempty"`
	MemoryUsage  *float64           `json:"memoryUsage,omitempty"`
	DiskUsage    *float64           `json:"diskUsage,omitempty"`
	Temperature  *float64           `json:"temperature,omitempty"`
	NetworkStats []api.NetworkStats `json:"networkStats"`
	CPU          *api.CPUStats      `json:"cpu,omitempty"`
	Memory       *api.MemoryStats   `json:"memory,omitempty"`
	Disk         *api.DiskStats     `json:"disk,omitempty"`
	Networks     []api.NetworkStats `json:"networks,omitempty"`
}

type taskAcceptedEvent struct {
	Type      string `json:"type"`
	TaskID    string `json:"taskId"`
	Timestamp string `json:"timestamp"`
}

type taskStartedEvent struct {
	Type      string `json:"type"`
	TaskID    string `json:"taskId"`
	Timestamp string `json:"timestamp"`
}

type taskLogEvent struct {
	Type      string `json:"type"`
	TaskID    string `json:"taskId"`
	Stream    string `json:"stream"`
	Line      string `json:"line"`
	Timestamp string `json:"timestamp"`
}

type taskCompletedEvent struct {
	Type       string `json:"type"`
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
	Authenticated bool                           `json:"authenticated"`
	NodeID        string                         `json:"nodeId,omitempty"`
	RootAccess    *api.RootAccessDesiredSnapshot `json:"rootAccess,omitempty"`
}

type AuthAckPayload = authAckEvent

type rootAccessUpdatedEvent struct {
	Type       string                         `json:"type"`
	RootAccess *api.RootAccessDesiredSnapshot `json:"rootAccess,omitempty"`
}

type authErrorEvent struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

type terminalStartEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	RunAsRoot bool   `json:"runAsRoot,omitempty"`
}

type terminalInputEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Payload   string `json:"payload"`
}

type terminalResizeEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type terminalStopEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Reason    string `json:"reason,omitempty"`
}

type terminalOpenedEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	Timestamp string `json:"timestamp"`
}

type terminalOutputEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Direction string `json:"direction"`
	Payload   string `json:"payload"`
	Timestamp string `json:"timestamp"`
}

type terminalExitedEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	ExitCode  int    `json:"exitCode,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Timestamp string `json:"timestamp"`
}

type terminalErrorEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

func formatTimestampUTCMillis(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
