package agentctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

const (
	officialCDNReleaseManifestURL = "https://cdn.noderax.net/noderax-agent/releases/%s/release-manifest.json"
	officialGithubReleaseAPIURL   = "https://api.github.com/repos/noderax/noderax-agent/releases/tags/agent-v%s"
	releaseManifestAssetName      = "release-manifest.json"
	updateProgressRequestTimeout  = 10 * time.Second
	updateDownloadTimeout         = 10 * time.Minute
	updateServiceReadyTimeout     = 30 * time.Second
)

type updateOptions struct {
	TargetVersion string
	TargetID      string
	RequestFile   string
	Rollback      bool
	ApplyNow      bool
}

type releaseManifest struct {
	Version     string                   `json:"version"`
	PublishedAt string                   `json:"publishedAt"`
	Commit      string                   `json:"commit"`
	Channel     string                   `json:"channel"`
	Artifacts   releaseManifestArtifacts `json:"artifacts"`
}

type releaseManifestArtifacts struct {
	AMD64 *releaseManifestArtifact `json:"amd64,omitempty"`
	ARM64 *releaseManifestArtifact `json:"arm64,omitempty"`
}

type releaseManifestArtifact struct {
	BinaryURL string `json:"binaryUrl"`
	SHA256    string `json:"sha256"`
}

type githubReleaseResponse struct {
	Assets []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (c CLI) Update(ctx context.Context, args []string) error {
	spec, err := currentPlatformSpec()
	if err != nil {
		return err
	}
	if spec.RequiresRoot {
		if err := requireRoot(); err != nil {
			return err
		}
	}
	if runtime.GOOS != "linux" || spec.Manager != serviceManagerSystemd {
		return fmt.Errorf("agent self-update currently supports Linux hosts managed by systemd only")
	}

	options, err := parseUpdateOptions(args)
	if err != nil {
		return err
	}

	if options.ApplyNow {
		return c.applyManagedUpdate(ctx, spec, options)
	}

	if err := c.launchDetachedUpdate(ctx, spec, options); err != nil {
		_ = c.reportManagedUpdateProgress(spec, options, api.AgentUpdateProgressRequest{
			Status:          "failed",
			ProgressPercent: 0,
			Message:         err.Error(),
		})
		return err
	}

	_, _ = fmt.Fprintf(
		c.stdoutOrDefault(),
		"Detached updater launched for agent %s.\n",
		options.TargetVersion,
	)
	return nil
}

func parseUpdateOptions(args []string) (updateOptions, error) {
	var options updateOptions

	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&options.TargetVersion, "target-version", "", "")
	fs.StringVar(&options.TargetID, "target-id", "", "")
	fs.StringVar(&options.RequestFile, "request-file", "", "")
	fs.BoolVar(&options.Rollback, "rollback", false, "")
	fs.BoolVar(&options.ApplyNow, "apply-now", false, "")

	if err := fs.Parse(args); err != nil {
		return updateOptions{}, fmt.Errorf("parse update flags: %w", err)
	}
	if len(fs.Args()) > 0 {
		return updateOptions{}, fmt.Errorf(
			"unexpected update arguments: %s",
			strings.Join(fs.Args(), " "),
		)
	}
	options.TargetVersion = strings.TrimSpace(options.TargetVersion)
	options.TargetID = strings.TrimSpace(options.TargetID)
	options.RequestFile = strings.TrimSpace(options.RequestFile)

	if options.RequestFile != "" {
		if options.TargetVersion != "" || options.TargetID != "" || options.Rollback {
			return updateOptions{}, fmt.Errorf(
				"update request-file cannot be combined with --target-version, --target-id, or --rollback",
			)
		}

		requestOptions, err := consumeManagedUpdateRequest(options.RequestFile)
		if err != nil {
			return updateOptions{}, err
		}
		requestOptions.ApplyNow = options.ApplyNow
		return requestOptions, nil
	}

	if options.TargetVersion == "" {
		return updateOptions{}, fmt.Errorf("update requires --target-version")
	}
	if options.TargetID == "" {
		return updateOptions{}, fmt.Errorf("update requires --target-id")
	}

	return options, nil
}

func consumeManagedUpdateRequest(path string) (updateOptions, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return updateOptions{}, fmt.Errorf("read update request file %s: %w", path, err)
	}
	if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
		return updateOptions{}, fmt.Errorf("remove update request file %s: %w", path, removeErr)
	}

	var options updateOptions
	if err := json.Unmarshal(data, &options); err != nil {
		return updateOptions{}, fmt.Errorf("decode update request file %s: %w", path, err)
	}

	options.TargetVersion = strings.TrimSpace(options.TargetVersion)
	options.TargetID = strings.TrimSpace(options.TargetID)
	options.RequestFile = ""
	options.ApplyNow = false

	if options.TargetVersion == "" {
		return updateOptions{}, fmt.Errorf("update request file %s is missing targetVersion", path)
	}
	if options.TargetID == "" {
		return updateOptions{}, fmt.Errorf("update request file %s is missing targetId", path)
	}

	return options, nil
}

func (c CLI) launchDetachedUpdate(
	ctx context.Context,
	spec platformSpec,
	options updateOptions,
) error {
	if !commandExists("systemd-run") {
		return fmt.Errorf("systemd-run is required to launch the detached updater")
	}

	unitName := fmt.Sprintf(
		"noderax-agent-updater-%s",
		sanitizeSystemdUnitComponent(options.TargetID),
	)
	args := []string{
		"--unit",
		unitName,
		"--collect",
		"--property=Type=exec",
		spec.BinaryPath,
		"update",
		"--target-version",
		options.TargetVersion,
		"--target-id",
		options.TargetID,
		"--apply-now",
	}
	if options.Rollback {
		args = append(args, "--rollback")
	}

	cmd := exec.CommandContext(ctx, "systemd-run", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("launch detached updater: %w", err)
		}
		return fmt.Errorf("launch detached updater: %w: %s", err, message)
	}

	return nil
}

func (c CLI) applyManagedUpdate(
	ctx context.Context,
	spec platformSpec,
	options updateOptions,
) (err error) {
	cfg, err := loadManagedConfig(spec)
	if err != nil {
		return fmt.Errorf("load managed config: %w", err)
	}
	cfg.ConfigFile = managedConfigPath(spec)

	if strings.TrimSpace(cfg.NodeID) == "" || strings.TrimSpace(cfg.AgentToken) == "" {
		return fmt.Errorf("managed agent identity is missing; self-update requires a registered node id and agent token")
	}

	client := api.NewClient(cfg.APIURL, cfg.RequestTimeout)
	client.SetAgentNodeID(cfg.NodeID)
	client.SetAgentToken(cfg.AgentToken)

	progress := 0
	report := func(status string, nextProgress int, nextMessage string) {
		progress = nextProgress
		if reportErr := c.reportManagedUpdateProgress(spec, options, api.AgentUpdateProgressRequest{
			Status:          status,
			ProgressPercent: nextProgress,
			Message:         nextMessage,
		}); reportErr != nil {
			if c.Logger != nil {
				c.Logger.Warn("agent update progress report failed", "error", reportErr)
			}
		}
	}
	defer func() {
		if err == nil {
			return
		}
		report("failed", progress, err.Error())
	}()

	report(
		"downloading",
		20,
		fmt.Sprintf("Fetching the official agent %s release manifest.", options.TargetVersion),
	)

	httpClient := &http.Client{Timeout: updateDownloadTimeout}
	manifest, err := fetchReleaseManifest(ctx, httpClient, options.TargetVersion)
	if err != nil {
		return err
	}

	artifact, err := selectReleaseArtifact(manifest, runtime.GOARCH)
	if err != nil {
		return err
	}

	downloadPath, err := downloadReleaseArtifact(
		ctx,
		httpClient,
		artifact.BinaryURL,
		spec.InstallDir,
		options.TargetVersion,
	)
	if err != nil {
		return err
	}
	defer os.Remove(downloadPath)

	report(
		"verifying",
		45,
		fmt.Sprintf("Verifying the official %s binary checksum.", runtime.GOARCH),
	)
	if err := verifyArtifactChecksum(downloadPath, artifact.SHA256); err != nil {
		return err
	}

	report(
		"installing",
		70,
		"Replacing the managed noderax-agent binary.",
	)
	if err := installReleaseArtifact(downloadPath, spec.BinaryPath); err != nil {
		return err
	}
	if spec.SymlinkPath != "" {
		if err := ensureSymlink(spec.BinaryPath, spec.SymlinkPath); err != nil {
			return err
		}
	}
	if err := writePrivilegedUpdateHelper(spec); err != nil {
		return fmt.Errorf("refresh privileged update helper: %w", err)
	}
	if err := writeRootProfileHelper(spec); err != nil {
		return fmt.Errorf("refresh root profile helper: %w", err)
	}
	if err := writePackageMutationHelper(spec); err != nil {
		return fmt.Errorf("refresh package mutation helper: %w", err)
	}
	if err := writeOperationalLogScanHelper(spec); err != nil {
		return fmt.Errorf("refresh operational log scan helper: %w", err)
	}
	if err := writeTaskRootHelper(spec); err != nil {
		return fmt.Errorf("refresh task root helper: %w", err)
	}
	if err := writeBaseSudoers(spec); err != nil {
		return fmt.Errorf("refresh base sudoers: %w", err)
	}
	if err := reconcilePersistedRootAccessProfile(spec); err != nil {
		return fmt.Errorf("refresh applied root access profile: %w", err)
	}

	report(
		"restarting",
		90,
		fmt.Sprintf("Restarting %s.", spec.ServiceName),
	)
	restartCommand := exec.CommandContext(ctx, "systemctl", "restart", spec.ServiceName)
	restartOutput, restartErr := restartCommand.CombinedOutput()
	if restartErr != nil {
		restartMessage := strings.TrimSpace(string(restartOutput))
		if restartMessage == "" {
			return fmt.Errorf("restart service %s: %w", spec.ServiceName, restartErr)
		}
		return fmt.Errorf(
			"restart service %s: %w: %s",
			spec.ServiceName,
			restartErr,
			restartMessage,
		)
	}

	if err := waitForRestartedService(ctx, spec.ServiceName); err != nil {
		return err
	}

	report(
		"waiting_for_reconnect",
		95,
		fmt.Sprintf(
			"Restart requested. Waiting for the %s heartbeat to confirm agent %s.",
			spec.ServiceName,
			options.TargetVersion,
		),
	)

	return nil
}

func (c CLI) reportManagedUpdateProgress(
	spec platformSpec,
	options updateOptions,
	request api.AgentUpdateProgressRequest,
) error {
	cfg, err := loadManagedConfig(spec)
	if err != nil {
		return err
	}
	cfg.ConfigFile = managedConfigPath(spec)
	if strings.TrimSpace(cfg.NodeID) == "" || strings.TrimSpace(cfg.AgentToken) == "" {
		return fmt.Errorf("managed agent identity is missing")
	}

	client := api.NewClient(cfg.APIURL, cfg.RequestTimeout)
	client.SetAgentNodeID(cfg.NodeID)
	client.SetAgentToken(cfg.AgentToken)

	reportCtx, cancel := context.WithTimeout(context.Background(), updateProgressRequestTimeout)
	defer cancel()

	return client.ReportAgentUpdateProgress(reportCtx, options.TargetID, request)
}

func fetchReleaseManifest(
	ctx context.Context,
	client *http.Client,
	version string,
) (releaseManifest, error) {
	cdnURL := fmt.Sprintf(officialCDNReleaseManifestURL, version)
	manifest, err := fetchManifestFromURL(ctx, client, cdnURL, nil)
	if err == nil {
		return manifest, nil
	}

	releaseURL := fmt.Sprintf(officialGithubReleaseAPIURL, version)
	request, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if reqErr != nil {
		return releaseManifest{}, reqErr
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "noderax-agent")

	response, reqErr := client.Do(request)
	if reqErr != nil {
		return releaseManifest{}, fmt.Errorf(
			"fetch release manifest from CDN and GitHub: %w",
			err,
		)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return releaseManifest{}, fmt.Errorf(
			"fetch release manifest from CDN and GitHub: %w; github status=%d",
			err,
			response.StatusCode,
		)
	}

	var release githubReleaseResponse
	if decodeErr := json.NewDecoder(response.Body).Decode(&release); decodeErr != nil {
		return releaseManifest{}, fmt.Errorf("decode GitHub release metadata: %w", decodeErr)
	}

	for _, asset := range release.Assets {
		if asset.Name != releaseManifestAssetName || strings.TrimSpace(asset.BrowserDownloadURL) == "" {
			continue
		}
		return fetchManifestFromURL(ctx, client, asset.BrowserDownloadURL, map[string]string{
			"Accept":     "application/json",
			"User-Agent": "noderax-agent",
		})
	}

	return releaseManifest{}, fmt.Errorf(
		"release manifest asset was not found for agent %s",
		version,
	)
}

func fetchManifestFromURL(
	ctx context.Context,
	client *http.Client,
	url string,
	headers map[string]string,
) (releaseManifest, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return releaseManifest{}, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := client.Do(request)
	if err != nil {
		return releaseManifest{}, fmt.Errorf("fetch release manifest %s: %w", url, err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return releaseManifest{}, fmt.Errorf(
			"fetch release manifest %s: status=%d",
			url,
			response.StatusCode,
		)
	}

	var manifest releaseManifest
	if err := json.NewDecoder(response.Body).Decode(&manifest); err != nil {
		return releaseManifest{}, fmt.Errorf("decode release manifest %s: %w", url, err)
	}
	if strings.TrimSpace(manifest.Version) == "" || manifest.Channel != "tag" {
		return releaseManifest{}, fmt.Errorf("release manifest %s is invalid", url)
	}

	return manifest, nil
}

func selectReleaseArtifact(
	manifest releaseManifest,
	arch string,
) (releaseManifestArtifact, error) {
	switch arch {
	case "amd64":
		if manifest.Artifacts.AMD64 == nil {
			return releaseManifestArtifact{}, fmt.Errorf(
				"official release %s does not include an amd64 artifact",
				manifest.Version,
			)
		}
		return *manifest.Artifacts.AMD64, nil
	case "arm64":
		if manifest.Artifacts.ARM64 == nil {
			return releaseManifestArtifact{}, fmt.Errorf(
				"official release %s does not include an arm64 artifact",
				manifest.Version,
			)
		}
		return *manifest.Artifacts.ARM64, nil
	default:
		return releaseManifestArtifact{}, fmt.Errorf(
			"agent self-update does not support architecture %s",
			arch,
		)
	}
}

func downloadReleaseArtifact(
	ctx context.Context,
	client *http.Client,
	url string,
	directory string,
	version string,
) (string, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create install directory %s: %w", directory, err)
	}

	file, err := os.CreateTemp(
		directory,
		fmt.Sprintf(".noderax-agent-%s-*.tmp", sanitizeSystemdUnitComponent(version)),
	)
	if err != nil {
		return "", fmt.Errorf("create temporary download file: %w", err)
	}
	defer file.Close()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "noderax-agent")

	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download official agent artifact: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf(
			"download official agent artifact failed with status %d",
			response.StatusCode,
		)
	}

	if _, err := io.Copy(file, response.Body); err != nil {
		return "", fmt.Errorf("write downloaded artifact: %w", err)
	}
	if err := file.Chmod(0o755); err != nil {
		return "", fmt.Errorf("chmod downloaded artifact: %w", err)
	}

	return file.Name(), nil
}

func verifyArtifactChecksum(path string, expectedSHA string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open downloaded artifact: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("hash downloaded artifact: %w", err)
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(strings.TrimSpace(expectedSHA), actual) {
		return fmt.Errorf(
			"downloaded artifact checksum mismatch: expected %s, got %s",
			expectedSHA,
			actual,
		)
	}

	return nil
}

func installReleaseArtifact(downloadPath, binaryPath string) error {
	nextPath := binaryPath + ".next"
	_ = os.Remove(nextPath)

	if err := os.Rename(downloadPath, nextPath); err != nil {
		return fmt.Errorf("stage updated binary: %w", err)
	}
	if err := os.Chmod(nextPath, 0o755); err != nil {
		return fmt.Errorf("chmod staged binary: %w", err)
	}
	if err := os.Rename(nextPath, binaryPath); err != nil {
		return fmt.Errorf("replace managed binary: %w", err)
	}

	return nil
}

func sanitizeSystemdUnitComponent(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "unknown"
	}

	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			builder.WriteByte('-')
		}
	}

	cleaned := strings.Trim(builder.String(), "-")
	cleaned = strings.ReplaceAll(cleaned, "--", "-")
	if cleaned == "" {
		return "unknown"
	}
	if len(cleaned) > 48 {
		return cleaned[:48]
	}
	return cleaned
}

func waitForRestartedService(ctx context.Context, serviceName string) error {
	deadline := time.Now().Add(updateServiceReadyTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(
			checkCtx,
			"systemctl",
			"is-active",
			"--quiet",
			serviceName,
		)
		err := cmd.Run()
		cancel()
		if err == nil {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf(
				"service %s did not become active after restart: %s",
				serviceName,
				readServiceFailureDetail(ctx, serviceName),
			)
		}

		time.Sleep(1 * time.Second)
	}
}

func readServiceFailureDetail(ctx context.Context, serviceName string) string {
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		statusCtx,
		"journalctl",
		"-u",
		serviceName,
		"-n",
		"25",
		"--no-pager",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Sprintf("unable to read journal: %v", err)
		}
		return fmt.Sprintf("unable to read journal: %v: %s", err, message)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) == "" {
		return "journal did not return any recent entries"
	}

	const maxLines = 5
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	return strings.Join(lines, " | ")
}
