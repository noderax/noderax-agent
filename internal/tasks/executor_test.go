package tasks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

func TestShellExecutorTimeoutFor(t *testing.T) {
	t.Parallel()

	executor := NewShellExecutor(5 * time.Minute)

	tests := []struct {
		name string
		task api.Task
		want time.Duration
	}{
		{
			name: "top level timeout wins",
			task: api.Task{
				Type:           "shell.exec",
				TimeoutSeconds: 45,
				Payload:        mustJSON(t, ShellExecPayload{Command: "echo hello", TimeoutSeconds: 10}),
			},
			want: 45 * time.Second,
		},
		{
			name: "payload timeout seconds",
			task: api.Task{
				Type:    "shell.exec",
				Payload: mustJSON(t, ShellExecPayload{Command: "echo hello", TimeoutSeconds: 90}),
			},
			want: 90 * time.Second,
		},
		{
			name: "payload duration string",
			task: api.Task{
				Type:    "shell.exec",
				Payload: mustJSON(t, ShellExecPayload{Command: "echo hello", Timeout: "2m30s"}),
			},
			want: 150 * time.Second,
		},
		{
			name: "default timeout fallback",
			task: api.Task{
				Type:    "shell.exec",
				Payload: mustJSON(t, ShellExecPayload{Command: "echo hello"}),
			},
			want: 5 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := executor.TimeoutFor(tt.task)
			if got != tt.want {
				t.Fatalf("timeout mismatch: got %s want %s", got, tt.want)
			}
		})
	}
}

func mustJSON(t *testing.T, payload ShellExecPayload) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	return data
}
