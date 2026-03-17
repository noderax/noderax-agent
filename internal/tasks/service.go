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
	client         *api.Client
	logger         *slog.Logger
	interval       time.Duration
	requestTimeout time.Duration
	credentials    func() (string, string)
	executor       *ShellExecutor

	mu      sync.Mutex
	running map[string]struct{}
	wg      sync.WaitGroup
}

func NewService(
	client *api.Client,
	logger *slog.Logger,
	interval time.Duration,
	requestTimeout time.Duration,
	defaultTaskTimeout time.Duration,
	credentials func() (string, string),
) *Service {
	return &Service{
		client:         client,
		logger:         logger,
		interval:       interval,
		requestTimeout: requestTimeout,
		credentials:    credentials,
		executor:       NewShellExecutor(defaultTaskTimeout),
		running:        make(map[string]struct{}),
	}
}

func (s *Service) Run(ctx context.Context) error {
	s.poll(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
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
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

func (s *Service) poll(ctx context.Context) {
	nodeID, agentToken := s.credentials()
	if nodeID == "" || agentToken == "" {
		s.logger.Warn("task poll skipped because agent identity is missing")
		return
	}

	requestCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()

	response, err := s.client.PullTasks(requestCtx, api.PullTasksRequest{
		NodeID:     nodeID,
		AgentToken: agentToken,
		Limit:      10,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.logger.Error("task poll failed", "error", err)
		return
	}

	for _, task := range response.Tasks {
		if !s.markRunning(task.ID) {
			s.logger.Warn("task is already running, skipping duplicate", "task_id", task.ID)
			continue
		}

		s.wg.Add(1)
		go s.handleTask(ctx, task)
	}
}

func (s *Service) handleTask(parentCtx context.Context, task api.Task) {
	defer s.wg.Done()
	defer s.unmarkRunning(task.ID)

	nodeID, agentToken := s.credentials()
	if nodeID == "" || agentToken == "" {
		s.logger.Error("cannot execute task because agent identity is missing", "task_id", task.ID)
		return
	}

	if err := s.withRequestTimeout(func(ctx context.Context) error {
		return s.client.StartTask(ctx, api.StartTaskRequest{
			NodeID:     nodeID,
			AgentToken: agentToken,
			TaskID:     task.ID,
			StartedAt:  time.Now().UTC(),
		})
	}); err != nil {
		s.logger.Error("failed to mark task as running", "task_id", task.ID, "error", err)
		return
	}

	s.logger.Info("task started", "task_id", task.ID, "type", task.Type)

	logSink := newTaskLogSink(s.client, s.logger, s.requestTimeout, nodeID, agentToken, task.ID)
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

	completeErr := s.withRequestTimeout(func(ctx context.Context) error {
		return s.client.CompleteTask(ctx, api.CompleteTaskRequest{
			NodeID:      nodeID,
			AgentToken:  agentToken,
			TaskID:      task.ID,
			Status:      status,
			ExitCode:    result.ExitCode,
			Error:       errorMessage,
			CompletedAt: completedAt,
			DurationMS:  result.Duration.Milliseconds(),
		})
	})
	if completeErr != nil {
		s.logger.Error("failed to complete task", "task_id", task.ID, "error", completeErr)
	}
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

func (s *Service) withRequestTimeout(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.requestTimeout)
	defer cancel()
	return fn(ctx)
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

type taskLogSink struct {
	client         *api.Client
	logger         *slog.Logger
	requestTimeout time.Duration
	nodeID         string
	agentToken     string
	taskID         string
	ch             chan api.TaskLogEntry
	done           chan struct{}
}

func newTaskLogSink(
	client *api.Client,
	logger *slog.Logger,
	requestTimeout time.Duration,
	nodeID string,
	agentToken string,
	taskID string,
) *taskLogSink {
	sink := &taskLogSink{
		client:         client,
		logger:         logger,
		requestTimeout: requestTimeout,
		nodeID:         nodeID,
		agentToken:     agentToken,
		taskID:         taskID,
		ch:             make(chan api.TaskLogEntry, 256),
		done:           make(chan struct{}),
	}

	go sink.run()
	return sink
}

func (s *taskLogSink) Enqueue(stream, line string) {
	entry := api.TaskLogEntry{
		Timestamp: time.Now().UTC(),
		Stream:    stream,
		Line:      line,
	}

	select {
	case s.ch <- entry:
	default:
		s.logger.Warn("dropping task log entry because the buffer is full", "task_id", s.taskID)
	}
}

func (s *taskLogSink) Close() {
	close(s.ch)
	<-s.done
}

func (s *taskLogSink) run() {
	defer close(s.done)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	batch := make([]api.TaskLogEntry, 0, 20)
	flush := func() {
		if len(batch) == 0 {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), s.requestTimeout)
		err := s.client.SendTaskLogs(ctx, api.SendTaskLogsRequest{
			NodeID:     s.nodeID,
			AgentToken: s.agentToken,
			TaskID:     s.taskID,
			Entries:    append([]api.TaskLogEntry(nil), batch...),
		})
		cancel()

		if err != nil {
			s.logger.Warn("failed to send task logs", "task_id", s.taskID, "error", err)
		}

		batch = batch[:0]
	}

	for {
		select {
		case entry, ok := <-s.ch:
			if !ok {
				flush()
				return
			}

			batch = append(batch, entry)
			if len(batch) >= 20 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
