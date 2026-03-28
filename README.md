<p align="center">
  <img src="https://raw.githubusercontent.com/noderax/noderax-web/main/public/logo.webp" alt="Noderax" width="220" />
</p>
<h1 align="center">Noderax Agent</h1>

Noderax Agent is the Go-based node runtime for the platform. It enrolls a machine, opens the agent realtime socket, streams telemetry, claims tasks over HTTP long polling by default, and executes supported operations such as shell commands and package management.

## Highlights

- Enrollment-based node onboarding with approval tokens
- Background service support for Ubuntu and macOS
- Built-in CLI for install, start, stop, restart, status, and config updates
- Realtime Socket.IO session for agent auth, metrics, and lifecycle signaling
- HTTP long-poll task claiming as the primary execution path
- Heartbeat-based agent version, platform version, and kernel telemetry for fleet visibility
- Graceful cancellation with log-drain timeout handling
- Non-interactive execution environment with `PAGER=cat` and high `COLUMNS`
- Scheduled runs arrive as standard queued tasks, so no separate schedule runtime is required on the agent
- Persistent node identity storage

## Supported Platforms

- Ubuntu and Ubuntu-compatible Linux systems with `systemd`
- macOS systems with `launchd`
- Manual source-based execution on other developer environments

## Quick Install

Prerequisites:

- `git`
- `go`

```bash
git clone <repo-url> noderax-agent
cd noderax-agent
sudo ./scripts/install.sh
```

The installer:

- asks for the API URL
- asks for the enrollment email
- requests an enrollment token
- waits for approval from the web UI
- installs the agent as a background service
- starts it automatically

When you run the installer from a source checkout, it mirrors the entered values into repository-level `config.json` for local visibility. The managed service still reads its OS-specific config path.

## Installed Paths

### Ubuntu

- Binary: `/opt/noderax-agent/noderax-agent`
- Symlink: `/usr/local/bin/noderax-agent`
- Config: `/etc/noderax-agent/config.json`
- State: `/var/lib/noderax-agent/agent_identity.json`
- Service: `/etc/systemd/system/noderax-agent.service`

### macOS

- Binary: `/usr/local/lib/noderax-agent/noderax-agent`
- Symlink: `/usr/local/bin/noderax-agent`
- Config: `/usr/local/etc/noderax-agent/config.json`
- State: `/usr/local/var/lib/noderax-agent/agent_identity.json`
- Service: `/Library/LaunchDaemons/com.noderax.agent.plist`

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

## Task Execution Environment

The agent executes `shell.exec` tasks in a controlled non-interactive environment:

- `PAGER=cat`
- `COLUMNS=100000`
- graceful cancellation with log drain timeout
- scheduled tasks use the same execution path as manually queued tasks

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

- Ubuntu installation assumes `systemd`.
- macOS installation assumes `launchd` and requires `sudo`.
- The managed service config path can be overridden with `NODERAX_CONFIG_FILE`.
- API-side realtime task push is not the default control path; HTTP claiming should be considered the normal operating mode.
- When a node is put into maintenance mode from the control plane, the API stops assigning new work to that node while in-flight tasks are allowed to finish.
