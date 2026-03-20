package realtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sio "github.com/karagenc/socket.io-go"
	"github.com/noderax/noderax-agent/internal/api"
)

type integrationDispatcher struct {
	dispatchCh chan api.Task
}

func (d *integrationDispatcher) DispatchRealtimeTask(_ context.Context, task api.Task) bool {
	select {
	case d.dispatchCh <- task:
	default:
	}
	return true
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
		func(context.Context) {},
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
		if auth.Type != EventAgentAuth || auth.NodeID != "node-1" || auth.AgentToken != "token-1" {
			t.Fatalf("unexpected auth payload: %+v", auth)
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
