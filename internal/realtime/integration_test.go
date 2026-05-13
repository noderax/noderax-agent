package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	sio "github.com/karagenc/socket.io-go"
	"github.com/noderax/noderax-agent/internal/api"
)

type integrationDispatcher struct {
	dispatchCh chan api.Task
}

type terminalIntegrationDispatcher struct {
	startCh  chan terminalStartEvent
	startErr error
	svc      *Service
}

func (d *integrationDispatcher) DispatchRealtimeTask(_ context.Context, task api.Task) bool {
	select {
	case d.dispatchCh <- task:
	default:
	}
	return true
}

func (d *terminalIntegrationDispatcher) DispatchRealtimeTask(
	_ context.Context,
	_ api.Task,
) bool {
	return false
}

func (d *terminalIntegrationDispatcher) StartTerminalSession(
	ctx context.Context,
	sessionID string,
	cols int,
	rows int,
	runAsRoot bool,
) error {
	select {
	case d.startCh <- terminalStartEvent{
		SessionID: sessionID,
		Cols:      cols,
		Rows:      rows,
		RunAsRoot: runAsRoot,
	}:
	default:
	}

	if d.startErr != nil {
		return d.startErr
	}

	if d.svc != nil {
		return d.svc.TerminalOpened(ctx, sessionID, cols, rows, time.Now().UTC())
	}

	return nil
}

func TestRealtimeConnectAuthDispatchLifecycle(t *testing.T) {
	ioServer := sio.NewServer(nil)
	namespace := ioServer.Of("/agent-realtime")

	authCh := make(chan authEvent, 1)
	acceptedCh := make(chan map[string]any, 1)

	namespace.OnConnection(func(socket sio.ServerSocket) {
		socket.OnEvent(EventAgentAuth, func(payload map[string]any) {
			var auth authEvent
			b, _ := json.Marshal(payload)
			_ = json.Unmarshal(b, &auth)
			select {
			case authCh <- auth:
			default:
			}

			socket.Emit(EventAgentAuthAck, map[string]any{
				"authenticated": true,
				"nodeId":        auth.NodeID,
			})

			taskPayload := map[string]any{
				"type": EventTaskDispatch,
				"task": map[string]any{
					"id":             "task-123",
					"type":           "shell",
					"payload":        map[string]any{"cmd": "echo hi"},
					"timeoutSeconds": 30,
				},
			}
			socket.Emit(EventTaskDispatch, taskPayload)
		})

		socket.OnEvent(EventTaskAccepted, func(payload map[string]any) {
			select {
			case acceptedCh <- payload:
			default:
			}
		})
	})

	if err := ioServer.Run(); err != nil {
		t.Fatalf("socket server run: %v", err)
	}
	defer ioServer.Close()

	mux := http.NewServeMux()
	mux.Handle("/socket.io/", ioServer)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	dispatcher := &integrationDispatcher{dispatchCh: make(chan api.Task, 1)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc, err := NewService(
		httpServer.URL,
		"/agent-realtime",
		"/socket.io/",
		5*time.Second,
		10*time.Second,
		32,
		0.2,
		logger,
		func() (string, string) {
			return "node-1", "token-1"
		},
		dispatcher,
		func() *api.RootAccessAgentReport { return nil },
		func(context.Context, authAckEvent) {},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	svc.SetRuntimeAgentVersion("1.0.0")
	svc.SetRuntimeLocation(&api.NodeLocation{
		Provider: "aws",
		Source:   "cloud_metadata",
		Region:   "eu-central-1",
		Zone:     "eu-central-1a",
	})

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- svc.Run(runCtx)
	}()

	select {
	case auth := <-authCh:
		if auth.Type != EventAgentAuth || auth.NodeID != "node-1" || auth.AgentToken != "token-1" {
			t.Fatalf("unexpected auth payload: %+v", auth)
		}
		if auth.AgentVersion != "1.0.0" {
			t.Fatalf("unexpected auth payload: %+v", auth)
		}
		if auth.Location == nil || auth.Location.Region != "eu-central-1" {
			t.Fatalf("expected auth location payload, got %+v", auth.Location)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for auth event")
	}

	select {
	case task := <-dispatcher.dispatchCh:
		if task.ID != "task-123" {
			t.Fatalf("unexpected dispatched task ID: %q", task.ID)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for task.dispatch")
	}

	if err := svc.TaskAccepted(context.Background(), "task-123", time.Now()); err != nil {
		t.Fatalf("TaskAccepted() error = %v", err)
	}

	select {
	case payload := <-acceptedCh:
		if payload["type"] != EventTaskAccepted {
			t.Fatalf("unexpected accepted type payload: %+v", payload)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for task.accepted")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for service shutdown")
	}
}

func TestRealtimeTerminalStartLifecycle(t *testing.T) {
	ioServer := sio.NewServer(nil)
	namespace := ioServer.Of("/agent-realtime")

	authCh := make(chan authEvent, 1)
	openedCh := make(chan map[string]any, 1)

	namespace.OnConnection(func(socket sio.ServerSocket) {
		socket.OnEvent(EventAgentAuth, func(payload map[string]any) {
			var auth authEvent
			b, _ := json.Marshal(payload)
			_ = json.Unmarshal(b, &auth)
			select {
			case authCh <- auth:
			default:
			}

			socket.Emit(EventAgentAuthAck, map[string]any{
				"authenticated": true,
				"nodeId":        auth.NodeID,
			})

			socket.Emit(EventTerminalStart, map[string]any{
				"type":      EventTerminalStart,
				"sessionId": "session-123",
				"cols":      96,
				"rows":      28,
			})
		})

		socket.OnEvent(EventTerminalOpened, func(payload map[string]any) {
			select {
			case openedCh <- payload:
			default:
			}
		})
	})

	if err := ioServer.Run(); err != nil {
		t.Fatalf("socket server run: %v", err)
	}
	defer ioServer.Close()

	mux := http.NewServeMux()
	mux.Handle("/socket.io/", ioServer)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	dispatcher := &terminalIntegrationDispatcher{
		startCh: make(chan terminalStartEvent, 1),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc, err := NewService(
		httpServer.URL,
		"/agent-realtime",
		"/socket.io/",
		5*time.Second,
		10*time.Second,
		32,
		0.2,
		logger,
		func() (string, string) {
			return "node-1", "token-1"
		},
		dispatcher,
		func() *api.RootAccessAgentReport { return nil },
		func(context.Context, authAckEvent) {},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	dispatcher.svc = svc

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- svc.Run(runCtx)
	}()

	select {
	case auth := <-authCh:
		if auth.NodeID != "node-1" || auth.AgentToken != "token-1" {
			t.Fatalf("unexpected auth payload: %+v", auth)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for auth event")
	}

	select {
	case start := <-dispatcher.startCh:
		if start.SessionID != "session-123" || start.Cols != 96 || start.Rows != 28 {
			t.Fatalf("unexpected terminal.start payload: %+v", start)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for terminal.start")
	}

	select {
	case payload := <-openedCh:
		if payload["sessionId"] != "session-123" || payload["type"] != EventTerminalOpened {
			t.Fatalf("unexpected terminal.opened payload: %+v", payload)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for terminal.opened")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for service shutdown")
	}
}

func TestRealtimeTerminalStartErrorIsReported(t *testing.T) {
	ioServer := sio.NewServer(nil)
	namespace := ioServer.Of("/agent-realtime")

	terminalErrorCh := make(chan map[string]any, 1)

	namespace.OnConnection(func(socket sio.ServerSocket) {
		socket.OnEvent(EventAgentAuth, func(payload map[string]any) {
			var auth authEvent
			b, _ := json.Marshal(payload)
			_ = json.Unmarshal(b, &auth)

			socket.Emit(EventAgentAuthAck, map[string]any{
				"authenticated": true,
				"nodeId":        auth.NodeID,
			})

			socket.Emit(EventTerminalStart, map[string]any{
				"type":      EventTerminalStart,
				"sessionId": "session-error",
				"cols":      80,
				"rows":      24,
			})
		})

		socket.OnEvent(EventTerminalError, func(payload map[string]any) {
			select {
			case terminalErrorCh <- payload:
			default:
			}
		})
	})

	if err := ioServer.Run(); err != nil {
		t.Fatalf("socket server run: %v", err)
	}
	defer ioServer.Close()

	mux := http.NewServeMux()
	mux.Handle("/socket.io/", ioServer)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	dispatcher := &terminalIntegrationDispatcher{
		startCh:  make(chan terminalStartEvent, 1),
		startErr: errors.New("start failed"),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc, err := NewService(
		httpServer.URL,
		"/agent-realtime",
		"/socket.io/",
		5*time.Second,
		10*time.Second,
		32,
		0.2,
		logger,
		func() (string, string) {
			return "node-1", "token-1"
		},
		dispatcher,
		func() *api.RootAccessAgentReport { return nil },
		func(context.Context, authAckEvent) {},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- svc.Run(runCtx)
	}()

	select {
	case payload := <-terminalErrorCh:
		if payload["sessionId"] != "session-error" || payload["type"] != EventTerminalError {
			t.Fatalf("unexpected terminal.error payload: %+v", payload)
		}
		if payload["message"] != "start failed" {
			t.Fatalf("unexpected terminal.error message: %+v", payload)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for terminal.error")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for service shutdown")
	}
}

func TestRealtimeRootAccessUpdateTriggersDesiredSnapshotHandler(t *testing.T) {
	ioServer := sio.NewServer(nil)
	namespace := ioServer.Of("/agent-realtime")

	rootAccessCh := make(chan *api.RootAccessDesiredSnapshot, 1)
	authCh := make(chan authEvent, 2)
	var appliedReportMu sync.RWMutex
	var appliedReport *api.RootAccessAgentReport

	namespace.OnConnection(func(socket sio.ServerSocket) {
		socket.OnEvent(EventAgentAuth, func(payload map[string]any) {
			var auth authEvent
			b, _ := json.Marshal(payload)
			_ = json.Unmarshal(b, &auth)
			select {
			case authCh <- auth:
			default:
			}

			socket.Emit(EventAgentAuthAck, map[string]any{
				"authenticated": true,
				"nodeId":        auth.NodeID,
			})
			socket.Emit(EventRootAccessUpdated, map[string]any{
				"type": EventRootAccessUpdated,
				"rootAccess": map[string]any{
					"profile":   "operational_task",
					"updatedAt": "2026-04-04T17:40:00.000Z",
				},
			})
		})
	})

	if err := ioServer.Run(); err != nil {
		t.Fatalf("socket server run: %v", err)
	}
	defer ioServer.Close()

	mux := http.NewServeMux()
	mux.Handle("/socket.io/", ioServer)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	dispatcher := &integrationDispatcher{dispatchCh: make(chan api.Task, 1)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc, err := NewService(
		httpServer.URL,
		"/agent-realtime",
		"/socket.io/",
		5*time.Second,
		10*time.Second,
		32,
		0.2,
		logger,
		func() (string, string) {
			return "node-1", "token-1"
		},
		dispatcher,
		func() *api.RootAccessAgentReport {
			appliedReportMu.RLock()
			defer appliedReportMu.RUnlock()
			return appliedReport
		},
		func(_ context.Context, ack authAckEvent) {
			if ack.RootAccess == nil {
				return
			}
			appliedReportMu.Lock()
			appliedReport = &api.RootAccessAgentReport{
				AppliedProfile: ack.RootAccess.Profile,
				LastAppliedAt:  ack.RootAccess.UpdatedAt,
			}
			appliedReportMu.Unlock()
			select {
			case rootAccessCh <- ack.RootAccess:
			default:
			}
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- svc.Run(runCtx)
	}()

	select {
	case auth := <-authCh:
		if auth.RootAccess != nil {
			t.Fatalf("initial auth should not include updated root access report: %+v", auth)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for initial auth")
	}

	select {
	case snapshot := <-rootAccessCh:
		if snapshot.Profile != api.RootAccessProfileOperationalTask {
			t.Fatalf("unexpected root access profile: %+v", snapshot)
		}
		if snapshot.UpdatedAt != "2026-04-04T17:40:00.000Z" {
			t.Fatalf("unexpected root access timestamp: %+v", snapshot)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for root-access.updated")
	}

	select {
	case auth := <-authCh:
		if auth.RootAccess == nil {
			t.Fatalf("expected follow-up auth with root access report")
		}
		if auth.RootAccess.AppliedProfile != api.RootAccessProfileOperationalTask {
			t.Fatalf("unexpected follow-up root access report: %+v", auth.RootAccess)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for follow-up auth report")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for service shutdown")
	}
}
