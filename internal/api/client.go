package api

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

const apiV1Prefix = "/api/v1"

type Client struct {
	http *resty.Client
}

type RequestError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
	Body       string
}

func (e *RequestError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = strings.TrimSpace(e.Body)
	}
	if message == "" {
		message = "request failed"
	}
	if e.StatusCode == 401 || e.StatusCode == 403 {
		return fmt.Sprintf("%s %s: status=%d unauthorized message=%s", strings.ToUpper(e.Method), e.Path, e.StatusCode, message)
	}
	return fmt.Sprintf("%s %s: status=%d message=%s", strings.ToUpper(e.Method), e.Path, e.StatusCode, message)
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

func (c *Client) SetAgentNodeID(nodeID string) {
	if strings.TrimSpace(nodeID) == "" {
		return
	}
	c.http.SetHeader("x-agent-node-id", nodeID)
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

func (c *Client) ConsumeNodeInstall(
	ctx context.Context,
	request ConsumeNodeInstallRequest,
) (ConsumeNodeInstallResponse, error) {
	var response ConsumeNodeInstallResponse
	if err := c.post(ctx, apiPath("/node-installs/consume"), request, &response); err != nil {
		return ConsumeNodeInstallResponse{}, err
	}
	return response, nil
}

func (c *Client) ClaimTask(ctx context.Context, request ClaimTaskRequest) (ClaimTaskResponse, error) {
	var response ClaimTaskResponse
	if err := c.post(ctx, apiPath("/agent/tasks/claim"), request, &response); err != nil {
		return ClaimTaskResponse{}, err
	}
	return response, nil
}

func (c *Client) ReportTaskAccepted(ctx context.Context, request TaskAcceptedRequest) error {
	return c.post(ctx, apiPath(fmt.Sprintf("/agent/tasks/%s/accepted", url.PathEscape(request.TaskID))), request, nil)
}

func (c *Client) ReportTaskStarted(ctx context.Context, request TaskStartedRequest) error {
	return c.post(ctx, apiPath(fmt.Sprintf("/agent/tasks/%s/started", url.PathEscape(request.TaskID))), request, nil)
}

func (c *Client) ReportTaskLog(ctx context.Context, request TaskLogRequest) error {
	return c.post(ctx, apiPath(fmt.Sprintf("/agent/tasks/%s/logs", url.PathEscape(request.TaskID))), request, nil)
}

func (c *Client) ReportTaskCompleted(ctx context.Context, request TaskCompletedRequest) error {
	return c.post(ctx, apiPath(fmt.Sprintf("/agent/tasks/%s/completed", url.PathEscape(request.TaskID))), request, nil)
}

func (c *Client) GetTaskControl(ctx context.Context, taskID string) (TaskControlResponse, error) {
	var response TaskControlResponse
	if err := c.get(ctx, apiPath(fmt.Sprintf("/agent/tasks/%s/control", url.PathEscape(taskID))), &response); err != nil {
		return TaskControlResponse{}, err
	}
	return response, nil
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
		return newRequestError("post", path, response, apiErr)
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
		return newRequestError("get", path, response, apiErr)
	}

	return nil
}

func apiPath(path string) string {
	return apiV1Prefix + path
}

func formatAPIError(response *resty.Response, apiErr *ErrorResponse) string {
	status := response.StatusCode()
	message := strings.TrimSpace(apiErr.String())
	if message == "" {
		message = strings.TrimSpace(string(response.Body()))
	}
	if message == "" {
		message = response.Status()
	}
	if status == 401 || status == 403 {
		return fmt.Sprintf("status=%d unauthorized message=%s", status, message)
	}
	return fmt.Sprintf("status=%d message=%s", status, message)
}

func newRequestError(method, path string, response *resty.Response, apiErr *ErrorResponse) error {
	status := response.StatusCode()
	message := strings.TrimSpace(apiErr.String())
	body := strings.TrimSpace(string(response.Body()))
	if message == "" {
		message = body
	}
	if message == "" {
		message = response.Status()
	}

	return &RequestError{
		Method:     method,
		Path:       path,
		StatusCode: status,
		Message:    message,
		Body:       body,
	}
}
