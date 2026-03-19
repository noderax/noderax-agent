package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

const apiV1Prefix = "/api/v1"

type Client struct {
	http *resty.Client
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	httpClient := resty.New().
		SetBaseURL(strings.TrimRight(baseURL, "/")).
		SetTimeout(timeout).
		SetRetryCount(3).
		SetRetryWaitTime(time.Second).
		SetRetryMaxWaitTime(5*time.Second).
		SetHeader("Accept", "application/json").
		SetHeader("Content-Type", "application/json").
		AddRetryCondition(func(response *resty.Response, err error) bool {
			if err != nil {
				return true
			}

			return response.StatusCode() == 429 || response.StatusCode() >= 500
		})

	return &Client{http: httpClient}
}

func (c *Client) SetAgentToken(token string) {
	if strings.TrimSpace(token) == "" {
		return
	}
	c.http.SetAuthToken(token)
}

func (c *Client) Register(ctx context.Context, request RegisterRequest) (RegisterResponse, error) {
	var response RegisterResponse
	if err := c.post(ctx, apiPath("/agent/register"), request, &response); err != nil {
		return RegisterResponse{}, err
	}
	return response, nil
}

func (c *Client) InitiateEnrollment(ctx context.Context, request InitiateEnrollmentRequest) (InitiateEnrollmentResponse, error) {
	var response InitiateEnrollmentResponse
	if err := c.post(ctx, apiPath("/enrollments/initiate"), request, &response); err != nil {
		return InitiateEnrollmentResponse{}, err
	}
	return response, nil
}

func (c *Client) GetEnrollment(ctx context.Context, token string) (EnrollmentStatusResponse, error) {
	var response EnrollmentStatusResponse
	if err := c.get(ctx, apiPath(fmt.Sprintf("/enrollments/%s", token)), &response); err != nil {
		return EnrollmentStatusResponse{}, err
	}
	return response, nil
}

func (c *Client) Heartbeat(ctx context.Context, request HeartbeatRequest) error {
	return c.post(ctx, apiPath("/agent/heartbeat"), request, nil)
}

func (c *Client) SendMetrics(ctx context.Context, request MetricsRequest) error {
	return c.post(ctx, apiPath("/agent/metrics"), request, nil)
}

func (c *Client) PullTasks(ctx context.Context, request PullTasksRequest) (PullTasksResponse, error) {
	var response PullTasksResponse
	if err := c.post(ctx, apiPath("/agent/tasks/pull"), request, &response); err != nil {
		return PullTasksResponse{}, err
	}
	if response.Tasks == nil {
		response.Tasks = []Task{}
	}
	return response, nil
}

func (c *Client) StartTask(ctx context.Context, request StartTaskRequest) error {
	return c.post(ctx, apiPath(fmt.Sprintf("/agent/tasks/%s/start", request.TaskID)), request, nil)
}

func (c *Client) SendTaskLogs(ctx context.Context, request SendTaskLogsRequest) error {
	if len(request.Entries) == 0 {
		return nil
	}
	return c.post(ctx, apiPath(fmt.Sprintf("/agent/tasks/%s/logs", request.TaskID)), request, nil)
}

func (c *Client) CompleteTask(ctx context.Context, request CompleteTaskRequest) error {
	return c.post(ctx, apiPath(fmt.Sprintf("/agent/tasks/%s/complete", request.TaskID)), request, nil)
}

func (c *Client) post(ctx context.Context, path string, request any, result any) error {
	apiErr := &ErrorResponse{}
	req := c.http.R().
		SetContext(ctx).
		SetBody(request).
		SetError(apiErr)

	if result != nil {
		req = req.SetResult(result)
	}

	response, err := req.Post(path)
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}

	if response.IsError() {
		return fmt.Errorf("post %s: status=%d message=%s", path, response.StatusCode(), apiErr.String())
	}

	return nil
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	apiErr := &ErrorResponse{}
	req := c.http.R().
		SetContext(ctx).
		SetError(apiErr)

	if result != nil {
		req = req.SetResult(result)
	}

	response, err := req.Get(path)
	if err != nil {
		return fmt.Errorf("get %s: %w", path, err)
	}

	if response.IsError() {
		return fmt.Errorf("get %s: status=%d message=%s", path, response.StatusCode(), apiErr.String())
	}

	return nil
}

func apiPath(path string) string {
	return apiV1Prefix + path
}
