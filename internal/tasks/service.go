package tasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	taskClient     TaskPollingClient
	taskControl    TaskControlClient
	authClient     TaskAuthClient
	taskPollPeriod time.Duration

	mu      sync.Mutex
	running map[string]struct{}
	wg      sync.WaitGroup
}

type TaskPollingClient interface {
	ClaimTask(context.Context, api.ClaimTaskRequest) (api.ClaimTaskResponse, error)
}

type TaskControlClient interface {
	GetTaskControl(context.Context, string) (api.TaskControlResponse, error)
}

type TaskAuthClient interface {
	SetAgentToken(string)
	SetAgentNodeID(string)
}

const (
	taskClaimEndpoint        = "/api/v1/agent/tasks/claim"
	taskControlEndpoint      = "/api/v1/agent/tasks/{taskId}/control"
	taskControlPollInterval  = 2 * time.Second
)

var errTaskCancelledByControl = errors.New("task cancelled by control endpoint")

type RealtimeTaskEvents interface {
	TaskAccepted(context.Context, string, time.Time) error
	TaskStarted(context.Context, string, time.Time) error
	TaskLog(context.Context, string, string, string, time.Time) error
	TaskCompleted(context.Context, api.CompleteTaskRequest) error
	ReportLogDrop()
	ReportDispatchHandled()
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
	client, period := s.pollingConfig()
	clientState := "nil"
	if client != nil {
		clientState = "not-nil"
	}
	s.logger.Debug("task polling enabled", "interval", period, "task_client", clientState)
	if client == nil {
		s.logger.Error("task polling client is nil; task claiming cannot start")
		return fmt.Errorf("task polling client is nil")
	}
	go s.pollTasks(ctx, client, period)

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

func (s *Service) SetTaskAuthClient(client TaskAuthClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authClient = client
}

func (s *Service) SetTaskPollingClient(client TaskPollingClient, period time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.taskClient = client
	s.taskPollPeriod = period
}

func (s *Service) SetTaskControlClient(client TaskControlClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.taskControl = client
}

func (s *Service) SetRealtimeEvents(events RealtimeTaskEvents) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realtime = events
}

func (s *Service) DispatchRealtimeTask(ctx context.Context, task api.Task) bool {
	return s.dispatchTask(ctx, task, "realtime")
}

func (s *Service) dispatchTask(ctx context.Context, task api.Task, source string) bool {
	s.logger.Info("dispatch received", "task_id", task.ID, "type", task.Type)

	if source == "realtime" {
		if realtime := s.realtimeEvents(); realtime != nil {
			realtime.ReportDispatchHandled()
		}
	}

	if source == "polling" {
		s.logger.Debug("dispatch source", "task_id", task.ID, "source", source)
	}

	if !s.markRunning(task.ID) {
		s.logger.Warn("task is already running, skipping duplicate", "task_id", task.ID)
		return false
	}

	s.wg.Add(1)
	go s.handleTask(ctx, task, time.Now())
	return true
}

func (s *Service) pollingConfig() (TaskPollingClient, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskClient, s.taskPollPeriod
}

func (s *Service) authConfig() TaskAuthClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authClient
}

func (s *Service) taskControlConfig() TaskControlClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskControl
}

func (s *Service) pollTasks(ctx context.Context, client TaskPollingClient, period time.Duration) {
	if period <= 0 {
		period = 15 * time.Second
	}

	waitMS := int(period.Milliseconds())
	if waitMS <= 0 {
		waitMS = 15000
	}

	networkBackoff := time.Second

	for {
		if err := ctx.Err(); err != nil {
			return
		}

		nodeID, _ := s.credentials()
		claimRequest := api.ClaimTaskRequest{MaxTasks: 1, WaitMS: waitMS}
		s.logger.Debug("claiming task over HTTP", "node_id", nodeID, "endpoint", taskClaimEndpoint, "max_tasks", claimRequest.MaxTasks, "wait_ms", claimRequest.WaitMS)

		claimTimeout := s.requestTimeout + period + time.Second
		claimCtx, cancel := context.WithTimeout(ctx, claimTimeout)
		response, err := client.ClaimTask(claimCtx, claimRequest)
		cancel()
		if err != nil {
			if reqErr, ok := asRequestError(err); ok {
				s.logger.Warn("task claim failed", "endpoint", taskClaimEndpoint, "status_code", reqErr.StatusCode, "message", reqErr.Message, "body", reqErr.Body)
			} else {
				s.logger.Warn("task claim failed", "endpoint", taskClaimEndpoint, "error", err)
			}

			if isAuthError(err) {
				nodeID, token := s.credentials()
				authClient := s.authConfig()
				if authClient != nil {
					authClient.SetAgentToken(token)
					authClient.SetAgentNodeID(nodeID)
					s.logger.Warn("task claim auth failed; refreshed agent auth headers and retrying once", "endpoint", taskClaimEndpoint, "node_id", nodeID)
				} else {
					s.logger.Error("task claim auth failed but task auth client is nil; cannot refresh headers")
				}

				retryCtx, retryCancel := context.WithTimeout(ctx, claimTimeout)
				retryResponse, retryErr := client.ClaimTask(retryCtx, claimRequest)
				retryCancel()
				if retryErr == nil {
					response = retryResponse
					err = nil
				} else {
					err = retryErr
					if reqErr, ok := asRequestError(err); ok {
						s.logger.Warn("task claim retry failed", "endpoint", taskClaimEndpoint, "status_code", reqErr.StatusCode, "message", reqErr.Message, "body", reqErr.Body)
					} else {
						s.logger.Warn("task claim retry failed", "endpoint", taskClaimEndpoint, "error", err)
					}
				}
			}

			if err != nil {
				if isAuthError(err) {
					s.logger.Error("task claim unauthorized after retry; stopping polling loop", "endpoint", taskClaimEndpoint)
					return
				}

				if isStateConflict(err) {
					s.logger.Warn("task claim state conflict; skipping to next claim", "endpoint", taskClaimEndpoint)
					continue
				}

				if isNetworkError(err) {
					s.logger.Warn("task claim network error; backing off", "endpoint", taskClaimEndpoint, "backoff", networkBackoff)
					if !sleepWithContext(ctx, networkBackoff) {
						return
					}
					networkBackoff = minDuration(networkBackoff*2, 15*time.Second)
					continue
				}

				s.logger.Warn("task claim remains unavailable; realtime dispatch fallback is disabled", "endpoint", taskClaimEndpoint)
				if !sleepWithContext(ctx, minDuration(period/3, 2*time.Second)) {
					return
				}
				continue
			}
		}

		networkBackoff = time.Second

		if response.Task == nil || response.Task.ID == "" {
			s.logger.Debug("task claim returned empty", "endpoint", taskClaimEndpoint)
			continue
		}

		s.logger.Info("task claim succeeded", "task_id", response.Task.ID, "task_type", response.Task.Type)
		s.dispatchTask(ctx, *response.Task, "polling")
	}
}

func asRequestError(err error) (*api.RequestError, bool) {
	var reqErr *api.RequestError
	if errors.As(err, &reqErr) {
		return reqErr, true
	}
	return nil, false
}

func isAuthError(err error) bool {
	reqErr, ok := asRequestError(err)
	if !ok {
		return false
	}
	return reqErr.StatusCode == 401 || reqErr.StatusCode == 403
}

func isStateConflict(err error) bool {
	reqErr, ok := asRequestError(err)
	if !ok {
		return false
	}
	return reqErr.StatusCode == 404 || reqErr.StatusCode == 409
}

func isNetworkError(err error) bool {
	_, ok := asRequestError(err)
	return !ok
}

func (s *Service) handleTask(parentCtx context.Context, task api.Task, receivedAt time.Time) {
	defer s.wg.Done()
	defer s.unmarkRunning(task.ID)

	realtime := s.realtimeEvents()
	if realtime == nil {
		s.logger.Error("cannot execute task because realtime events are unavailable", "task_id", task.ID)
		return
	}

	nodeID, agentToken := s.credentials()

	var status = "failed"
	var errorMessage = ""
	var exitCode = -1
	var duration time.Duration
	var resultData any
	var outputData string

	// Always ensure task is completed
	defer func() {
		completedAt := time.Now().UTC()
		
		pkgCount := 0
		hasResult := resultData != nil
		if hasResult {
			if resultMap, ok := resultData.(map[string]any); ok {
				if pkgs, ok := resultMap["packages"].([]PackageInfo); ok {
					pkgCount = len(pkgs)
				} else if res, ok := resultMap["results"].([]PackageInfo); ok {
					pkgCount = len(res)
				}
			}
		}

		if errorMessage != "" {
			s.logger.Warn("task finished with error", "task_id", task.ID, "status", status, "error", errorMessage, "has_result", hasResult)
		} else {
			s.logger.Info("task completed", "task_id", task.ID, "exit_code", exitCode, "duration", duration, "has_result", hasResult, "pkg_count", pkgCount)
		}

		completeErr := realtime.TaskCompleted(parentCtx, api.CompleteTaskRequest{
			TaskID:      task.ID,
			Status:      status,
			ExitCode:    exitCode,
			Error:       errorMessage,
			CompletedAt: completedAt,
			DurationMS:  duration.Milliseconds(),
			Result:      resultData,
			Output:      outputData,
		})
		
		if completeErr == nil {
			s.logger.Info("task completed sent", "task_id", task.ID, "status", status, "pkg_count", pkgCount)
		} else {
			s.logger.Error("failed to send task completed event", "task_id", task.ID, "error", completeErr)
		}
	}()

	watchdog := make(chan struct{})
	go func() {
		select {
		case <-watchdog:
			return
		case <-time.After(2 * time.Second):
			s.logger.Warn("watchdog: queued task not started within 2s", "task_id", task.ID, "type", task.Type)
		}
	}()

	acceptedAt := time.Now().UTC()
	if err := realtime.TaskAccepted(parentCtx, task.ID, acceptedAt); err != nil {
		s.logger.Warn("failed to emit realtime task.accepted event", "task_id", task.ID, "error", err)
	} else {
		s.logger.Info("task accepted sent", "task_id", task.ID)
	}

	if nodeID == "" || agentToken == "" {
		errorMessage = "cannot execute task because agent identity is missing"
		s.logger.Error(errorMessage, "task_id", task.ID)
		close(watchdog)
		return
	}

	startedAt := time.Now().UTC()
	if err := realtime.TaskStarted(parentCtx, task.ID, startedAt); err != nil {
		s.logger.Warn("failed to emit realtime task.started event", "task_id", task.ID, "error", err)
	} else {
		s.logger.Info("task started sent", "task_id", task.ID)
	}
	close(watchdog)

	s.logger.Info("task started", "task_id", task.ID, "type", task.Type)

	logSink := newRealtimeTaskLogSink(realtime, s.logger, task.ID)
	taskCtx, cancel := context.WithTimeout(parentCtx, s.executor.TimeoutFor(task))
	defer cancel()

	controlDone := make(chan struct{})
	var controlMu sync.Mutex
	controlCancelled := false
	controlCancelReason := ""
	markCancelled := func(reason string) {
		controlMu.Lock()
		defer controlMu.Unlock()
		if controlCancelled {
			return
		}
		controlCancelled = true
		controlCancelReason = strings.TrimSpace(reason)
	}
	controlState := func() (bool, string) {
		controlMu.Lock()
		defer controlMu.Unlock()
		return controlCancelled, controlCancelReason
	}

	if controlClient := s.taskControlConfig(); controlClient != nil {
		go s.watchTaskControl(taskCtx, task.ID, controlClient, cancel, markCancelled, controlDone)
	}

	result, execErr := s.executor.Execute(taskCtx, task, logSink.Enqueue)
	close(controlDone)
	logSink.Close()

	cancelledByControl, cancelReason := controlState()
	if cancelledByControl && (execErr == nil || errors.Is(execErr, context.Canceled)) {
		execErr = errTaskCancelledByControl
	}

	status = taskStatus(execErr)
	if execErr != nil {
		errorMessage = execErr.Error()
	}
	exitCode = result.ExitCode
	duration = result.Duration
	resultData = result.Result
	outputData = result.Output

	if errors.Is(execErr, errTaskCancelledByControl) {
		if cancelReason != "" {
			errorMessage = cancelReason
		}
		if strings.TrimSpace(outputData) == "" {
			if cancelReason != "" {
				outputData = fmt.Sprintf("task cancelled by control request: %s", cancelReason)
			} else {
				outputData = "task cancelled by control request"
			}
		}
	}
}

func (s *Service) watchTaskControl(
	ctx context.Context,
	taskID string,
	client TaskControlClient,
	cancel func(),
	markCancelled func(string),
	done <-chan struct{},
) {
	ticker := time.NewTicker(taskControlPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			checkCtx, checkCancel := context.WithTimeout(ctx, s.requestTimeout)
			control, err := client.GetTaskControl(checkCtx, taskID)
			checkCancel()
			if err != nil {
				if reqErr, ok := asRequestError(err); ok {
					s.logger.Warn("task control check failed", "endpoint", taskControlEndpoint, "task_id", taskID, "status_code", reqErr.StatusCode, "message", reqErr.Message, "body", reqErr.Body)
				} else {
					s.logger.Warn("task control check failed", "endpoint", taskControlEndpoint, "task_id", taskID, "error", err)
				}
				continue
			}

			s.logger.Debug("task control response", "endpoint", taskControlEndpoint, "task_id", taskID, "status", control.Status, "cancel_requested", control.CancelRequested)

			if control.CancelRequested {
				reason := strings.TrimSpace(control.CancelReason)
				markCancelled(reason)
				s.logger.Warn("task cancellation requested", "task_id", taskID, "reason", reason)
				cancel()
				return
			}
		}
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
	case errors.Is(err, errTaskCancelledByControl):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "failed"
	}
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
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
		s.realtime.ReportLogDrop()
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
