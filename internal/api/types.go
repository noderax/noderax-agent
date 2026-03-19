package api

import (
	"encoding/json"
	"time"
)

type RegisterRequest struct {
	EnrollmentToken string `json:"enrollmentToken,omitempty"`
	Hostname        string `json:"hostname"`
	OperatingSystem string `json:"operatingSystem"`
	Platform        string `json:"platform,omitempty"`
	PlatformVersion string `json:"platformVersion,omitempty"`
	KernelVersion   string `json:"kernelVersion,omitempty"`
	Architecture    string `json:"architecture"`
	AgentVersion    string `json:"agentVersion"`
}

type RegisterResponse struct {
	NodeID     string `json:"nodeId"`
	AgentToken string `json:"agentToken"`
}

type EnrollmentAdditionalInfo struct {
	OS           string `json:"os,omitempty"`
	Arch         string `json:"arch,omitempty"`
	AgentVersion string `json:"agentVersion,omitempty"`
}

type InitiateEnrollmentRequest struct {
	Email          string                   `json:"email"`
	Hostname       string                   `json:"hostname"`
	AdditionalInfo EnrollmentAdditionalInfo `json:"additionalInfo"`
}

type InitiateEnrollmentResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

type EnrollmentStatusResponse struct {
	Status     string `json:"status"`
	NodeID     string `json:"nodeId,omitempty"`
	AgentToken string `json:"agentToken,omitempty"`
}

type HeartbeatRequest struct {
	NodeID       string    `json:"nodeId"`
	AgentToken   string    `json:"agentToken"`
	AgentVersion string    `json:"agentVersion"`
	SentAt       time.Time `json:"sentAt"`
}

type CPUStats struct {
	UsagePercent float64 `json:"usagePercent"`
}

type MemoryStats struct {
	TotalBytes     uint64  `json:"totalBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	FreeBytes      uint64  `json:"freeBytes"`
	UsedPercent    float64 `json:"usedPercent"`
	AvailableBytes uint64  `json:"availableBytes"`
}

type DiskStats struct {
	Path        string  `json:"path"`
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	FreeBytes   uint64  `json:"freeBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

type NetworkStats struct {
	Interface   string `json:"interface"`
	BytesSent   uint64 `json:"bytesSent"`
	BytesRecv   uint64 `json:"bytesRecv"`
	PacketsSent uint64 `json:"packetsSent"`
	PacketsRecv uint64 `json:"packetsRecv"`
	ErrorsIn    uint64 `json:"errorsIn"`
	ErrorsOut   uint64 `json:"errorsOut"`
	DropIn      uint64 `json:"dropIn"`
	DropOut     uint64 `json:"dropOut"`
}

type MetricsRequest struct {
	NodeID      string         `json:"nodeId"`
	AgentToken  string         `json:"agentToken"`
	CollectedAt time.Time      `json:"collectedAt"`
	CPU         CPUStats       `json:"cpu"`
	Memory      MemoryStats    `json:"memory"`
	Disk        DiskStats      `json:"disk"`
	Networks    []NetworkStats `json:"networks"`
}

type PullTasksRequest struct {
	NodeID     string `json:"nodeId"`
	AgentToken string `json:"agentToken"`
	Limit      int    `json:"limit"`
}

type PullTasksResponse struct {
	Tasks []Task `json:"tasks"`
}

type Task struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	TimeoutSeconds int             `json:"timeoutSeconds,omitempty"`
}

type StartTaskRequest struct {
	NodeID     string    `json:"nodeId"`
	AgentToken string    `json:"agentToken"`
	TaskID     string    `json:"taskId"`
	StartedAt  time.Time `json:"startedAt"`
}

type TaskLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"`
	Line      string    `json:"line"`
}

type SendTaskLogsRequest struct {
	NodeID     string         `json:"nodeId"`
	AgentToken string         `json:"agentToken"`
	TaskID     string         `json:"taskId"`
	Entries    []TaskLogEntry `json:"entries"`
}

type CompleteTaskRequest struct {
	NodeID      string    `json:"nodeId"`
	AgentToken  string    `json:"agentToken"`
	TaskID      string    `json:"taskId"`
	Status      string    `json:"status"`
	ExitCode    int       `json:"exitCode"`
	Error       string    `json:"error,omitempty"`
	CompletedAt time.Time `json:"completedAt"`
	DurationMS  int64     `json:"durationMs"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func (e ErrorResponse) String() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Error != "" {
		return e.Error
	}
	return "request failed"
}
