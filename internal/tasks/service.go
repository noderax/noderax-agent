package tasks

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

type Service struct {
	logger         *slog.Logger
	requestTimeout time.Duration
	credentials    func() (string, string)
	executor       *ShellExecutor
	realtime       RealtimeTaskEvents

	mu      sync.Mutex
	running map[string]struct{}
	wg      sync.WaitGroup
}

type RealtimeTaskEvents interface {
	TaskAccepted(context.Context, string, time.Time) error
	TaskStarted(context.Context, string, time.Time) error
	TaskLog(context.Context, string, string, string, time.Time) error
	TaskCompleted(context.Context, api.CompleteTaskRequest) error
}

func NewService(
	logger *slog.Logger,
	requestTimeout time.Duration,
	defaultTaskTimeout time.Duration,
	credentials func() (string, string),
) *Service {
	return &Service{
		logger:         logger,
		requestTimeout: requestTimeout,
		credentials:    credentials,
		executor:       NewShellExecutor(defaultTaskTimeout),
		running:        make(map[string]struct{}),
	}
}

func (s *Service) Run(ctx context.Context) error {
	<-ctx.Done()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(s.requestTimeout):
		s.logger.Warn("timed out waiting for running tasks to stop")
		return nil
	}
}

func (s *Service) SetRealtimeEvents(events RealtimeTaskEvents) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realtime = events
}

func (s *Service) DispatchRealtimeTask(ctx context.Context, task api.Task) bool {
	if !s.markRunning(task.ID) {
		s.logger.Warn("task is already running, skipping duplicate", "task_id", task.ID)
		return false
	}

	s.wg.Add(1)
	go s.handleTask(ctx, task)
	return true
}

func (s *Service) handleTask(parentCtx context.Context, task api.Task) {
	defer s.wg.Done()
	defer s.unmarkRunning(task.ID)

	nodeID, agentToken := s.credentials()
	if nodeID == "" || agentToken == "" {
		s.logger.Error("cannot execute task because agent identity is missing", "task_id", task.ID)
		return
	}

	realtime := s.realtimeEvents()
	if realtime == nil {
		s.logger.Error("cannot execute task because realtime events are unavailable", "task_id", task.ID)
		return
	}

	startedAt := time.Now().UTC()
	if err := realtime.TaskAccepted(parentCtx, task.ID, startedAt); err != nil {
		s.logger.Warn("failed to emit realtime task.accepted event", "task_id", task.ID, "error", err)
	}
	if err := realtime.TaskStarted(parentCtx, task.ID, startedAt); err != nil {
		s.logger.Warn("failed to emit realtime task.started event", "task_id", task.ID, "error", err)
	}

	s.logger.Info("task started", "task_id", task.ID, "type", task.Type)

	logSink := newRealtimeTaskLogSink(realtime, s.logger, task.ID)
	taskCtx, cancel := context.WithTimeout(parentCtx, s.executor.TimeoutFor(task))
	defer cancel()

	result, execErr := s.executor.Execute(taskCtx, task, logSink.Enqueue)
	logSink.Close()

	status := taskStatus(execErr)
	errorMessage := ""
	if execErr != nil {
		errorMessage = execErr.Error()
		s.logger.Warn("task finished with error", "task_id", task.ID, "status", status, "error", execErr)
	} else {
		s.logger.Info("task completed", "task_id", task.ID, "exit_code", result.ExitCode, "duration", result.Duration)
	}

	completedAt := result.CompletedAt
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}

	completeErr := realtime.TaskCompleted(parentCtx, api.CompleteTaskRequest{
		NodeID:      nodeID,
		AgentToken:  agentToken,
		TaskID:      task.ID,
		Status:      status,
		ExitCode:    result.ExitCode,
		Error:       errorMessage,
		CompletedAt: completedAt,
		DurationMS:  result.Duration.Milliseconds(),
	})
	if completeErr != nil {
		s.logger.Error("failed to complete task", "task_id", task.ID, "error", completeErr)
	}
}

func (s *Service) realtimeEvents() RealtimeTaskEvents {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.realtime
}

func (s *Service) markRunning(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.running[taskID]; exists {
		return false
	}

	s.running[taskID] = struct{}{}
	return true
}

func (s *Service) unmarkRunning(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, taskID)
}

func taskStatus(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "failed"
	}
}

type realtimeTaskLogSink struct {
	realtime RealtimeTaskEvents
	logger   *slog.Logger
	taskID   string
	ch       chan api.TaskLogEntry
	done     chan struct{}
}

func newRealtimeTaskLogSink(realtime RealtimeTaskEvents, logger *slog.Logger, taskID string) *realtimeTaskLogSink {
	sink := &realtimeTaskLogSink{
		realtime: realtime,
		logger:   logger,
		taskID:   taskID,
		ch:       make(chan api.TaskLogEntry, 512),
		done:     make(chan struct{}),
	}

	go sink.run()
	return sink
}

func (s *realtimeTaskLogSink) Enqueue(stream, line string) {
	entry := api.TaskLogEntry{
		Timestamp: time.Now().UTC(),
		Stream:    stream,
		Line:      line,
	}

	select {
	case s.ch <- entry:
	default:
		s.logger.Warn("dropping realtime task.log event because the buffer is full", "task_id", s.taskID)
	}
}

func (s *realtimeTaskLogSink) Close() {
	close(s.ch)
	<-s.done
}

func (s *realtimeTaskLogSink) run() {
	defer close(s.done)

	for entry := range s.ch {
		if err := s.realtime.TaskLog(context.Background(), s.taskID, entry.Stream, entry.Line, entry.Timestamp); err != nil {
			s.logger.Warn("failed to emit realtime task.log event", "task_id", s.taskID, "error", err)
		}
	}
}
