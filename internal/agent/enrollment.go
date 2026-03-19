package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/config"
	"github.com/noderax/noderax-agent/internal/system"
)

const (
	defaultEnrollmentPollInterval = 5 * time.Second
	defaultEnrollmentWaitTimeout  = 10 * time.Minute
)

type enrollmentClient interface {
	InitiateEnrollment(context.Context, api.InitiateEnrollmentRequest) (api.InitiateEnrollmentResponse, error)
	GetEnrollment(context.Context, string) (api.EnrollmentStatusResponse, error)
}

type enrollmentStatusClient interface {
	GetEnrollment(context.Context, string) (api.EnrollmentStatusResponse, error)
}

func RunInteractiveEnrollment(
	ctx context.Context,
	cfg config.Config,
	client enrollmentClient,
	logger *slog.Logger,
	version string,
	input io.Reader,
	output io.Writer,
) error {
	if input == nil {
		return fmt.Errorf("interactive enrollment requires an input reader")
	}
	if output == nil {
		output = io.Discard
	}

	store := NewIdentityStore(cfg.StateFile)
	if identity, err := store.Load(); err == nil && identity.Ready() {
		_, _ = fmt.Fprintf(output, "Agent is already enrolled with node ID %s.\n", identity.NodeID)
		return nil
	} else if err != nil && !errors.Is(err, ErrIdentityNotFound) {
		return err
	}

	hostInfo, err := system.HostInfo(ctx)
	if err != nil && logger != nil {
		logger.Warn("host info collection returned partial data", "error", err)
	}

	reader := bufio.NewReader(input)

	email, err := promptValue(reader, output, "Email address", true)
	if err != nil {
		return err
	}

	requestCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()

	operatingSystem := hostInfo.Platform
	if strings.TrimSpace(operatingSystem) == "" {
		operatingSystem = hostInfo.OS
	}

	response, err := client.InitiateEnrollment(requestCtx, api.InitiateEnrollmentRequest{
		Email:    email,
		Hostname: hostInfo.Hostname,
		AdditionalInfo: api.EnrollmentAdditionalInfo{
			OS:           operatingSystem,
			Arch:         hostInfo.Architecture,
			AgentVersion: version,
		},
	})
	if err != nil {
		return fmt.Errorf("initiate enrollment: %w", err)
	}
	if strings.TrimSpace(response.Token) == "" {
		return fmt.Errorf("initiate enrollment: API returned an empty token")
	}

	cfg.ConfigFile = configPathForWrite(cfg)
	cfg.EnrollmentToken = response.Token

	if err := config.SaveFile(cfg.ConfigFile, cfg); err != nil {
		return fmt.Errorf("persist pending enrollment token: %w", err)
	}

	_, _ = fmt.Fprintf(output, "\nEnrollment token: %s\n", response.Token)
	if !response.ExpiresAt.IsZero() {
		_, _ = fmt.Fprintf(output, "Token expires at: %s\n", response.ExpiresAt.Format(time.RFC3339))
	}
	_, _ = fmt.Fprintf(output, "Token saved to: %s\n", cfg.ConfigFile)
	_, _ = fmt.Fprintln(output, "Open the web UI, enter this token, and approve the node.")

	_, _ = fmt.Fprintln(output, "Waiting for enrollment approval...")

	identity, err := completeEnrollment(ctx, cfg, client, logger, store, response.Token, defaultEnrollmentPollInterval, defaultEnrollmentWaitTimeout)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(output, "Enrollment approved. Node ID: %s\n", identity.NodeID)
	return nil
}

func completeEnrollment(
	ctx context.Context,
	cfg config.Config,
	client enrollmentStatusClient,
	logger *slog.Logger,
	store *IdentityStore,
	token string,
	pollInterval time.Duration,
	waitTimeout time.Duration,
) (Identity, error) {
	waitCtx := ctx
	cancel := func() {}
	if waitTimeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, waitTimeout)
	}
	defer cancel()

	identity, err := waitForEnrollmentApproval(waitCtx, client, logger, cfg.RequestTimeout, token, pollInterval)
	if err != nil {
		return Identity{}, err
	}

	if err := store.Save(identity); err != nil {
		return Identity{}, fmt.Errorf("persist identity: %w", err)
	}

	if strings.TrimSpace(cfg.ConfigFile) != "" {
		cfg.EnrollmentToken = ""
		if err := config.SaveFile(cfg.ConfigFile, cfg); err != nil && logger != nil {
			logger.Warn("failed to clear enrollment token from config", "path", cfg.ConfigFile, "error", err)
		}
	}

	return identity, nil
}

func waitForEnrollmentApproval(
	ctx context.Context,
	client enrollmentStatusClient,
	logger *slog.Logger,
	requestTimeout time.Duration,
	token string,
	pollInterval time.Duration,
) (Identity, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Identity{}, fmt.Errorf("enrollment token is required")
	}
	if pollInterval <= 0 {
		pollInterval = defaultEnrollmentPollInterval
	}
	if requestTimeout <= 0 {
		requestTimeout = 10 * time.Second
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		statusCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		status, err := client.GetEnrollment(statusCtx, token)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return Identity{}, ctx.Err()
			}
			if logger != nil {
				logger.Warn("enrollment status poll failed", "token", token, "error", err)
			}
		} else {
			identity, done, statusErr := identityFromEnrollmentStatus(status)
			if done {
				return identity, statusErr
			}
		}

		select {
		case <-ctx.Done():
			return Identity{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func identityFromEnrollmentStatus(status api.EnrollmentStatusResponse) (Identity, bool, error) {
	state := strings.ToLower(strings.TrimSpace(status.Status))
	switch state {
	case "approved":
		if strings.TrimSpace(status.NodeID) == "" || strings.TrimSpace(status.AgentToken) == "" {
			return Identity{}, true, fmt.Errorf("enrollment approved without node credentials")
		}
		return Identity{
			NodeID:       status.NodeID,
			AgentToken:   status.AgentToken,
			RegisteredAt: time.Now().UTC(),
		}, true, nil
	case "", "pending":
		return Identity{}, false, nil
	case "revoked":
		return Identity{}, true, fmt.Errorf("enrollment revoked")
	case "rejected", "denied":
		return Identity{}, true, fmt.Errorf("enrollment rejected: %s", enrollmentMessage(status))
	case "expired":
		return Identity{}, true, fmt.Errorf("enrollment token expired")
	default:
		message := enrollmentMessage(status)
		if message == "" {
			message = status.Status
		}
		return Identity{}, true, fmt.Errorf("unexpected enrollment status: %s", message)
	}
}

func enrollmentMessage(status api.EnrollmentStatusResponse) string {
	return "no details provided"
}

func configPathForWrite(cfg config.Config) string {
	if strings.TrimSpace(cfg.ConfigFile) != "" {
		return cfg.ConfigFile
	}
	return "./config.json"
}

func promptValue(reader *bufio.Reader, output io.Writer, label string, required bool) (string, error) {
	prompt := label
	if !required {
		prompt += " (optional)"
	}

	for {
		if _, err := fmt.Fprintf(output, "%s: ", prompt); err != nil {
			return "", err
		}

		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}

		trimmed := strings.TrimSpace(value)
		if trimmed != "" || !required {
			return trimmed, nil
		}

		if _, writeErr := fmt.Fprintf(output, "%s is required.\n", label); writeErr != nil {
			return "", writeErr
		}

		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s is required", strings.ToLower(label))
		}
	}
}
