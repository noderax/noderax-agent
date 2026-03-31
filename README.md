<p align="center">
  <img src="https://raw.githubusercontent.com/noderax/noderax-web/main/public/logo.webp" alt="Noderax" width="220" />
</p>
<h1 align="center">Noderax Agent</h1>

Noderax Agent is the Go-based node runtime for the platform. It enrolls a machine, opens the agent realtime socket, streams telemetry, claims tasks over HTTP long polling by default, and executes supported operations such as shell commands and package management.

## Highlights

- Enrollment-based node onboarding with approval tokens
- One-click bootstrap installer for Ubuntu and Debian
- Workspace-generated bootstrap tokens for the `Add node` flow
- Background service support for Linux-based systems
- Dedicated `noderax` runtime user for the service and remote operations
- Built-in CLI for install, start, stop, restart, status, and config updates
- Realtime Socket.IO session for agent auth, metrics, and lifecycle signaling
- HTTP long-poll task claiming as the primary execution path
- Interactive terminal session support over the agent realtime socket
- Heartbeat-based agent version, platform version, and kernel telemetry for fleet visibility
- Graceful cancellation with log-drain timeout handling
- Non-interactive execution environment with `PAGER=cat` and high `COLUMNS`
- PTY shell fallback modes for hosts where the default controlling-TTY start path is restricted
- Scheduled runs arrive as standard queued tasks, so no separate schedule runtime is required on the agent
- Persistent node identity storage

## Supported Platforms

- Ubuntu and Debian hosts with `systemd`
- Manual source-based execution on other developer environments

## Quick Install

The preferred onboarding path is from the web dashboard:

1. Open `Nodes` for the target workspace.
2. Choose `Add node`.
3. Fill in node metadata in step 1.
4. Copy the generated install command from step 2.

```bash
curl -fsSL https://cdn.noderax.net/noderax-agent/install.sh | sudo bash -s -- --api-url https://api.example.com --bootstrap-token <token>
```

The installer:

- checks and installs required packages
- creates the `noderax` system user
- grants passwordless `sudo` to `noderax`
- downloads the correct prebuilt agent binary
- bootstraps the node with the provided token
- installs and starts the background service automatically

## R2 Release Automation

Agent release assets can be published to Cloudflare R2 automatically by the GitHub Actions workflow at [`.github/workflows/agent-release.yml`](./.github/workflows/agent-release.yml).

Expected R2 object layout:

- `noderax-agent/install.sh`
- `noderax-agent/releases/latest/noderax-agent-linux-amd64`
- `noderax-agent/releases/latest/noderax-agent-linux-arm64`
- `noderax-agent/releases/latest/SHA256SUMS`
- `noderax-agent/releases/<version>/noderax-agent-linux-amd64`
- `noderax-agent/releases/<version>/noderax-agent-linux-arm64`
- `noderax-agent/releases/<version>/SHA256SUMS`

Required GitHub secrets:

- `R2_ACCOUNT_ID`
- `R2_ACCESS_KEY_ID`
- `R2_SECRET_ACCESS_KEY`
- `R2_BUCKET`

`R2_BUCKET` plain bucket name olmalı. URL veya custom domain verme.

Doğru örnek:

- `R2_BUCKET=noderax-assets`

Yanlış örnekler:

- `R2_BUCKET=https://cdn.noderax.net`
- `R2_BUCKET=https://<accountid>.r2.cloudflarestorage.com/noderax-assets`

Workflow publish öncesi bucket varlığını da kontrol eder. `NoSuchBucket` alırsan genelde şu üç sebepten biridir:

- `R2_BUCKET` değeri gerçek bucket adı değildir
- `R2_ACCOUNT_ID` farklı bir Cloudflare hesabına aittir
- access key çifti bucket’ın bulunduğu hesaba ait değildir

Trigger behavior:

- Pushes to `main` refresh `install.sh` and the `latest` binaries
- Tags matching `agent-v*` publish a versioned channel and also refresh `latest`
- Manual runs can publish any URL-safe release slug through `workflow_dispatch`

The workflow builds Linux `amd64` and `arm64` binaries, injects version metadata into the Go binary, and uploads the artifacts directly to R2 through the S3-compatible endpoint.

## Installed Paths

### Linux (systemd)

- Binary: `/opt/noderax-agent/noderax-agent`
- Symlink: `/usr/local/bin/noderax-agent`
- Config: `/etc/noderax-agent/config.json`
- State: `/var/lib/noderax-agent/agent_identity.json`
- Service: `/etc/systemd/system/noderax-agent.service`
- Runtime user: `noderax`
- Sudoers: `/etc/sudoers.d/noderax-agent`

## Non-Interactive Bootstrap From A Local Binary

If you already have the binary on the target host, you can run the same bootstrap flow without the shell installer:

```bash
sudo ./noderax-agent install --non-interactive --api-url https://api.example.com --bootstrap-token <token>
```

## Manual Installation

### 1. Build the agent

```bash
go build -o noderax-agent ./cmd/agent
```

### 2. Prepare a config file

```bash
cp config.example.json config.json
```

Set at least:

- `api_url`

Leave `enrollment_token` empty. The enrollment command will populate it.

### 3. Run enrollment

```bash
./noderax-agent enroll
```

The agent will:

- ask for the enrollment email
- call `POST /api/v1/enrollments/initiate`
- save the returned token
- poll `GET /api/v1/enrollments/{token}`
- persist the approved `nodeId` and `agentToken`

### 4. Start the agent manually

```bash
./noderax-agent
```

Use a custom config path if needed:

```bash
NODERAX_CONFIG_FILE=/path/to/config.json ./noderax-agent
```

## Service Management

```bash
sudo noderax-agent start
sudo noderax-agent stop
sudo noderax-agent restart
sudo noderax-agent status
```

## Configuration Management

Show active config:

```bash
noderax-agent config show
```

Update config values:

```bash
sudo noderax-agent config set api_url https://api.example.com
sudo noderax-agent config set task_timeout 30s
sudo noderax-agent config set metrics_interval 15s
sudo noderax-agent config set log_level debug
```

Supported keys:

- `api_url`
- `enrollment_token`
- `node_id`
- `agent_token`
- `heartbeat_interval`
- `metrics_interval`
- `task_poll_interval`
- `request_timeout`
- `task_timeout`
- `shutdown_timeout`
- `realtime_enabled`
- `realtime_ping_interval`
- `realtime_queue_size`
- `realtime_backoff_jitter`
- `realtime_namespace`
- `realtime_path`
- `state_file`
- `log_level`

## Task Delivery Model

The current default execution path is HTTP polling.

- The agent long-polls `POST /api/v1/agent/tasks/claim`
- A claimed task is acknowledged with `accepted` and `started`
- Logs are appended through `POST /api/v1/agent/tasks/:taskId/logs`
- Completion is posted to `POST /api/v1/agent/tasks/:taskId/completed`
- Cancellation intent is checked through agent control polling

The agent realtime socket remains important for:

- agent authentication
- metrics streaming
- lifecycle support
- interactive terminal control and streaming
- optional compatibility with API-side realtime task dispatch when explicitly enabled

Fleet visibility uses heartbeat telemetry only. The current product surface does not include agent self-update orchestration inside the agent runtime; upgrades remain an external deployment concern.

## Realtime Socket.IO v4

Typical realtime settings:

```bash
NODERAX_API_URL=https://<domain>
NODERAX_REALTIME_NAMESPACE=/agent-realtime
NODERAX_REALTIME_PATH=/socket.io/
NODERAX_REALTIME_PING_INTERVAL=2s
NODERAX_REALTIME_METRICS_INTERVAL=3s
```

Notes:

- Auth is performed after socket connection with `agent.auth`
- Namespace and Engine.IO path are configured separately
- Startup performs a polling health-check against `/socket.io/` before the full socket session is used

Common failure modes:

- `invalid URL input`
- `tls/proxy handshake failure`
- `namespace connect failure`
- `auth error`

## Interactive Terminal Sessions

Interactive terminal sessions are not delivered through the HTTP task claim loop. They use the agent realtime socket directly.

Incoming control events:

- `terminal.start`
- `terminal.input`
- `terminal.resize`
- `terminal.stop`

Outgoing lifecycle events:

- `terminal.opened`
- `terminal.output`
- `terminal.exited`
- `terminal.error`

Implementation notes:

- The agent opens a PTY-backed interactive shell rather than reusing the non-interactive task executor
- Shell resolution tries `$SHELL`, `/bin/bash`, `/bin/zsh`, then `/bin/sh`
- Unix PTY startup falls back across multiple modes:
  - `pty+ctty`
  - `pty-no-ctty`
  - `pty-minimal`
- The terminal session environment sets `TERM=xterm-256color` and `PAGER=cat`
- Output is chunked and flushed on a timer so the web console receives live updates without waiting for more keystrokes
- On constrained hosts, fallback PTY modes may log reduced job-control capability while remaining usable

## Task Execution Environment

The agent executes `shell.exec` tasks in a controlled non-interactive environment:

- `PAGER=cat`
- `COLUMNS=100000`
- graceful cancellation with log drain timeout
- scheduled tasks use the same execution path as manually queued tasks
- when installed through the bootstrap installer, tasks run under the `noderax` user
- package operations can elevate through passwordless `sudo -n` provided to `noderax`

For package listing on Debian/Ubuntu, the agent uses optimized `dpkg -l` parsing to return structured package metadata.

## Project Structure

- [`cmd/agent`](cmd/agent): application entrypoint and CLI dispatch
- [`internal/agent`](internal/agent): enrollment, identity persistence, bootstrap
- [`internal/agentctl`](internal/agentctl): install, service management, config CLI
- [`internal/api`](internal/api): HTTP client and API models
- [`internal/tasks`](internal/tasks): HTTP claim loop and task execution
- [`internal/metrics`](internal/metrics): metrics worker
- [`internal/realtime`](internal/realtime): Socket.IO client and handlers
- [`internal/system`](internal/system): host and system information helpers
- [`scripts`](scripts): installation entrypoints

## Development

Run tests:

```bash
go test ./...
```

Build locally:

```bash
go build -o noderax-agent ./cmd/agent
```

Run in foreground:

```bash
cp config.example.json config.json
./noderax-agent enroll
./noderax-agent
```

## Notes

- Linux installation assumes `systemd`.

- The managed service config path can be overridden with `NODERAX_CONFIG_FILE`.
- API-side realtime task push is not the default control path; HTTP claiming should be considered the normal operating mode.
- Interactive terminals are the main exception: they depend on the agent realtime socket being healthy.
- When a node is put into maintenance mode from the control plane, the API stops assigning new work to that node while in-flight tasks are allowed to finish.
