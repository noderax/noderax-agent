package tasks

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

type TaskLifecycleAPIClient interface {
	ReportTaskAccepted(context.Context, api.TaskAcceptedRequest) error
	ReportTaskStarted(context.Context, api.TaskStartedRequest) error
	ReportTaskLog(context.Context, api.TaskLogRequest) error
	ReportTaskCompleted(context.Context, api.TaskCompletedRequest) error
}

type HTTPTaskEvents struct {
	client TaskLifecycleAPIClient
	logger *slog.Logger
}

const (
	taskLifecycleRetryAttempts = 4
	taskLifecycleRetryDelay    = 150 * time.Millisecond
	taskLogMaxLineChars        = 5000
	taskLogTruncatedSuffix     = "...[truncated]"
)

func NewHTTPTaskEvents(client TaskLifecycleAPIClient, logger *slog.Logger) *HTTPTaskEvents {
	return &HTTPTaskEvents{client: client, logger: logger}
}

func (h *HTTPTaskEvents) TaskAccepted(ctx context.Context, taskID string, timestamp time.Time) error {
	req := api.TaskAcceptedRequest{
		TaskID:    taskID,
		Timestamp: formatTimestampUTCMillis(timestamp),
	}
	h.logger.Debug("task lifecycle request", "endpoint", "/api/v1/agent/tasks/{taskId}/accepted", "task_id", taskID)
	err := h.withQueuedStateRetry(ctx, func() error {
		return h.client.ReportTaskAccepted(ctx, req)
	})
	h.logResult("/api/v1/agent/tasks/{taskId}/accepted", taskID, err)
	return err
}

func (h *HTTPTaskEvents) TaskStarted(ctx context.Context, taskID string, timestamp time.Time) error {
	req := api.TaskStartedRequest{
		TaskID:    taskID,
		Timestamp: formatTimestampUTCMillis(timestamp),
	}
	h.logger.Debug("task lifecycle request", "endpoint", "/api/v1/agent/tasks/{taskId}/started", "task_id", taskID)
	err := h.withQueuedStateRetry(ctx, func() error {
		return h.client.ReportTaskStarted(ctx, req)
	})
	h.logResult("/api/v1/agent/tasks/{taskId}/started", taskID, err)
	return err
}

func (h *HTTPTaskEvents) TaskLog(ctx context.Context, taskID, stream, line string, timestamp time.Time) error {
	req := api.TaskLogRequest{
		TaskID:    taskID,
		Stream:    stream,
		Line:      normalizeTaskLogLine(line),
		Timestamp: formatTimestampUTCMillis(timestamp),
	}
	h.logger.Debug("task lifecycle request", "endpoint", "/api/v1/agent/tasks/{taskId}/logs", "task_id", taskID, "stream", stream)
	err := h.withQueuedStateRetry(ctx, func() error {
		return h.client.ReportTaskLog(ctx, req)
	})
	h.logResult("/api/v1/agent/tasks/{taskId}/logs", taskID, err)
	return err
}

func (h *HTTPTaskEvents) TaskCompleted(ctx context.Context, event api.CompleteTaskRequest) error {
	req := api.TaskCompletedRequest{
		TaskID:     event.TaskID,
		Status:     event.Status,
		Result:     event.Result,
		Output:     event.Output,
		ExitCode:   event.ExitCode,
		Error:      event.Error,
		Timestamp:  formatTimestampUTCMillis(event.CompletedAt),
		DurationMS: event.DurationMS,
	}
	h.logger.Debug("task lifecycle request", "endpoint", "/api/v1/agent/tasks/{taskId}/completed", "task_id", event.TaskID, "status", event.Status)
	err := h.withQueuedStateRetry(ctx, func() error {
		return h.client.ReportTaskCompleted(ctx, req)
	})
	h.logResult("/api/v1/agent/tasks/{taskId}/completed", event.TaskID, err)
	return err
}

func (h *HTTPTaskEvents) ReportLogDrop() {}

func (h *HTTPTaskEvents) ReportDispatchHandled() {}

func formatTimestampUTCMillis(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func (h *HTTPTaskEvents) logResult(endpoint, taskID string, err error) {
	if err == nil {
		h.logger.Debug("task lifecycle response", "endpoint", endpoint, "task_id", taskID, "status_code", "2xx")
		return
	}

	var reqErr *api.RequestError
	if errors.As(err, &reqErr) {
		h.logger.Warn("task lifecycle response", "endpoint", endpoint, "task_id", taskID, "status_code", reqErr.StatusCode, "message", reqErr.Message, "body", reqErr.Body)
		return
	}

	h.logger.Warn("task lifecycle response", "endpoint", endpoint, "task_id", taskID, "error", err)
}

func normalizeTaskLogLine(line string) string {
	runeCount := 0
	for range line {
		runeCount++
	}

	if runeCount <= taskLogMaxLineChars {
		return line
	}

	suffixRunes := 0
	for range taskLogTruncatedSuffix {
		suffixRunes++
	}

	maxPrefixRunes := taskLogMaxLineChars - suffixRunes
	if maxPrefixRunes < 0 {
		maxPrefixRunes = 0
	}

	var builder strings.Builder
	builder.Grow(taskLogMaxLineChars)

	prefixRunes := 0
	for _, char := range line {
		if prefixRunes >= maxPrefixRunes {
			break
		}
		builder.WriteRune(char)
		prefixRunes++
	}

	builder.WriteString(taskLogTruncatedSuffix)
	return builder.String()
}

func (h *HTTPTaskEvents) withQueuedStateRetry(ctx context.Context, send func() error) error {
	var err error
	for attempt := 1; attempt <= taskLifecycleRetryAttempts; attempt++ {
		err = send()
		if err == nil || !isQueuedStateConflict(err) {
			return err
		}

		if attempt == taskLifecycleRetryAttempts {
			return err
		}

		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}

		h.logger.Warn(
			"task lifecycle request queued-state conflict; retrying",
			"attempt",
			attempt+1,
			"max_attempts",
			taskLifecycleRetryAttempts,
		)

		if ctx == nil {
			time.Sleep(taskLifecycleRetryDelay)
			continue
		}

		if !sleepWithContext(ctx, taskLifecycleRetryDelay) {
			return ctx.Err()
		}
	}

	return err
}

func isQueuedStateConflict(err error) bool {
	reqErr, ok := asRequestError(err)
	if !ok || reqErr.StatusCode != 409 {
		return false
	}

	message := strings.ToLower(strings.TrimSpace(reqErr.Message + " " + reqErr.Body))
	return strings.Contains(message, "queued state")
}
