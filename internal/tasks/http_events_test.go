package tasks

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

type lifecycleClientStub struct {
	acceptedReqs  []api.TaskAcceptedRequest
	startedReqs   []api.TaskStartedRequest
	logReqs       []api.TaskLogRequest
	completedReqs []api.TaskCompletedRequest

	acceptedErrors  []error
	startedErrors   []error
	logErrors       []error
	completedErrors []error
}

func (s *lifecycleClientStub) ReportTaskAccepted(ctx context.Context, req api.TaskAcceptedRequest) error {
	s.acceptedReqs = append(s.acceptedReqs, req)
	return popLifecycleError(&s.acceptedErrors)
}

func (s *lifecycleClientStub) ReportTaskStarted(ctx context.Context, req api.TaskStartedRequest) error {
	s.startedReqs = append(s.startedReqs, req)
	return popLifecycleError(&s.startedErrors)
}

func (s *lifecycleClientStub) ReportTaskLog(ctx context.Context, req api.TaskLogRequest) error {
	s.logReqs = append(s.logReqs, req)
	return popLifecycleError(&s.logErrors)
}

func (s *lifecycleClientStub) ReportTaskCompleted(ctx context.Context, req api.TaskCompletedRequest) error {
	s.completedReqs = append(s.completedReqs, req)
	return popLifecycleError(&s.completedErrors)
}

func popLifecycleError(errorsSlice *[]error) error {
	if len(*errorsSlice) == 0 {
		return nil
	}
	err := (*errorsSlice)[0]
	*errorsSlice = (*errorsSlice)[1:]
	return err
}

func queuedConflictRequestError(path string) error {
	return &api.RequestError{
		Method:     "post",
		Path:       path,
		StatusCode: 409,
		Message:    "Task is in queued state and cannot accept lifecycle updates",
	}
}

func TestHTTPTaskEventsTaskLogTruncatesLongLine(t *testing.T) {
	t.Parallel()

	stub := &lifecycleClientStub{}
	events := NewHTTPTaskEvents(stub, slog.New(slog.NewTextHandler(io.Discard, nil)))

	line := strings.Repeat("x", 5100)
	err := events.TaskLog(context.Background(), "task-1", "stdout", line, time.Now().UTC())
	if err != nil {
		t.Fatalf("TaskLog returned error: %v", err)
	}
	if len(stub.logReqs) != 1 {
		t.Fatalf("expected one log request, got %d", len(stub.logReqs))
	}

	sent := stub.logReqs[0].Line
	runeCount := 0
	for range sent {
		runeCount++
	}
	if runeCount > taskLogMaxLineChars {
		t.Fatalf("expected log line <= %d chars, got %d", taskLogMaxLineChars, runeCount)
	}
	if !strings.HasSuffix(sent, taskLogTruncatedSuffix) {
		t.Fatalf("expected truncated suffix %q, got %q", taskLogTruncatedSuffix, sent)
	}
}

func TestHTTPTaskEventsTaskStartedRetriesQueuedConflict(t *testing.T) {
	t.Parallel()

	stub := &lifecycleClientStub{
		startedErrors: []error{
			queuedConflictRequestError("/api/v1/agent/tasks/task-1/started"),
			nil,
		},
	}
	events := NewHTTPTaskEvents(stub, slog.New(slog.NewTextHandler(io.Discard, nil)))

	err := events.TaskStarted(context.Background(), "task-1", time.Now().UTC())
	if err != nil {
		t.Fatalf("TaskStarted returned error: %v", err)
	}
	if len(stub.startedReqs) != 2 {
		t.Fatalf("expected two started attempts, got %d", len(stub.startedReqs))
	}
}

func TestHTTPTaskEventsTaskLogRetriesQueuedConflict(t *testing.T) {
	t.Parallel()

	stub := &lifecycleClientStub{
		logErrors: []error{
			queuedConflictRequestError("/api/v1/agent/tasks/task-1/logs"),
			nil,
		},
	}
	events := NewHTTPTaskEvents(stub, slog.New(slog.NewTextHandler(io.Discard, nil)))

	err := events.TaskLog(context.Background(), "task-1", "stdout", "hello", time.Now().UTC())
	if err != nil {
		t.Fatalf("TaskLog returned error: %v", err)
	}
	if len(stub.logReqs) != 2 {
		t.Fatalf("expected two log attempts, got %d", len(stub.logReqs))
	}
}
