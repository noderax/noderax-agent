package tasks

import (
	"context"
	"errors"
	"log/slog"
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

func NewHTTPTaskEvents(client TaskLifecycleAPIClient, logger *slog.Logger) *HTTPTaskEvents {
	return &HTTPTaskEvents{client: client, logger: logger}
}

func (h *HTTPTaskEvents) TaskAccepted(ctx context.Context, taskID string, timestamp time.Time) error {
	req := api.TaskAcceptedRequest{
		TaskID:    taskID,
		Timestamp: formatTimestampUTCMillis(timestamp),
	}
	h.logger.Debug("task lifecycle request", "endpoint", "/api/v1/agent/tasks/{taskId}/accepted", "task_id", taskID)
	err := h.client.ReportTaskAccepted(ctx, req)
	h.logResult("/api/v1/agent/tasks/{taskId}/accepted", taskID, err)
	return err
}

func (h *HTTPTaskEvents) TaskStarted(ctx context.Context, taskID string, timestamp time.Time) error {
	req := api.TaskStartedRequest{
		TaskID:    taskID,
		Timestamp: formatTimestampUTCMillis(timestamp),
	}
	h.logger.Debug("task lifecycle request", "endpoint", "/api/v1/agent/tasks/{taskId}/started", "task_id", taskID)
	err := h.client.ReportTaskStarted(ctx, req)
	h.logResult("/api/v1/agent/tasks/{taskId}/started", taskID, err)
	return err
}

func (h *HTTPTaskEvents) TaskLog(ctx context.Context, taskID, stream, line string, timestamp time.Time) error {
	req := api.TaskLogRequest{
		TaskID:    taskID,
		Stream:    stream,
		Line:      line,
		Timestamp: formatTimestampUTCMillis(timestamp),
	}
	h.logger.Debug("task lifecycle request", "endpoint", "/api/v1/agent/tasks/{taskId}/logs", "task_id", taskID, "stream", stream)
	err := h.client.ReportTaskLog(ctx, req)
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
	err := h.client.ReportTaskCompleted(ctx, req)
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
