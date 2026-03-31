package tasks

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"log/slog"
	"os"

	"github.com/noderax/noderax-agent/internal/api"
)

type mockRealtimeEvents struct {
	mu            sync.Mutex
	acceptedCalls int
	startedCalls  int
	completed     *api.CompleteTaskRequest
}

func (m *mockRealtimeEvents) TaskAccepted(ctx context.Context, taskID string, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acceptedCalls++
	return nil
}

func (m *mockRealtimeEvents) TaskStarted(ctx context.Context, taskID string, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startedCalls++
	return nil
}

func (m *mockRealtimeEvents) TaskLog(ctx context.Context, taskID, stream, line string, t time.Time) error {
	return nil
}

func (m *mockRealtimeEvents) TaskCompleted(ctx context.Context, req api.CompleteTaskRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed = &req
	return nil
}

func (m *mockRealtimeEvents) ReportLogDrop()           {}
func (m *mockRealtimeEvents) ReportDispatchHandled()   {}

func newMockExecCommandRunner(ctx context.Context, name string, args ...string) commandRunner {
	if name == "dpkg" || name == "apt" || name == "apt-get" {
		return newHelperCommandRunner(
			ctx,
			"ii  bash           5.2.21-2ubuntu amd64        GNU Bourne Again SHell\n",
			"",
			0,
		)
	}
	return newExecCommandRunner(ctx, name, args...)
}

func TestTaskLifecycleGuarantees(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	
	// Create service with mock executor strategy
	svc := NewService(logger, 5*time.Second, 5*time.Second, func() (string, string) {
		return "node-123", "token-abc"
	})
	svc.executor.newCommand = newMockExecCommandRunner
	svc.executor.lookPath = func(s string) (string, error) {
		return s, nil
	}
	// Fake environment to trick ensureLinuxTaskSupport in packageListCommand
	svc.executor.goos = "linux"
	
	mockEvents := &mockRealtimeEvents{}
	svc.SetRealtimeEvents(mockEvents)

	// dispatch package list task
	task := api.Task{
		ID:      "task-abc",
		Type:    TaskTypePackageList,
		Payload: json.RawMessage(`{}`),
	}

	// Will block and run handleTask inside a goroutine
	accepted := svc.DispatchRealtimeTask(context.Background(), task)
	if !accepted {
		t.Fatal("task was not accepted by dispatcher")
	}

	// Wait for waitgroup
	svc.wg.Wait()

	// Verify transitions
	mockEvents.mu.Lock()
	defer mockEvents.mu.Unlock()

	if mockEvents.acceptedCalls != 1 {
		t.Errorf("TaskAccepted expected 1 call, got %d", mockEvents.acceptedCalls)
	}
	
	if mockEvents.startedCalls != 1 {
		t.Errorf("TaskStarted expected 1 call, got %d", mockEvents.startedCalls)
	}
	
	if mockEvents.completed == nil {
		t.Fatal("TaskCompleted was never called")
	}

	res := mockEvents.completed
	if res.Status != "success" {
		t.Errorf("expected success status, got %s. err: %s", res.Status, res.Error)
	}
	
	if res.Result == nil {
		t.Fatal("expected structured package result attached to payload, was nil")
	}
}
