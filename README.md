<p align="center">
  <img src="https://raw.githubusercontent.com/noderax/noderax-web/main/public/logo.webp" alt="Noderax" width="220" />
</p>
<h1 align="center">Noderax Agent</h1>

Noderax Agent is a Go-based node agent that connects servers to the Noderax platform. It enrolls a machine, opens a realtime websocket session, streams metrics, receives task dispatch events from `noderax-api`, and executes supported operations such as shell commands and package management.

## Highlights

- Enrollment-based node onboarding with short-lived approval tokens
- Background service support for Ubuntu and macOS
- Built-in CLI for install, start, stop, restart, status, and config updates
- Realtime websocket events for metrics and task lifecycle
- **Reliable Task Execution:** Support for task cancellation with log drain timeouts
- **Non-interactive Environment:** Commands run with `PAGER=cat` and high `COLUMNS` to ensure stable output
- Scheduled runs arrive as standard queued tasks, so no separate schedule runtime is required on the agent
- Persistent identity storage for approved nodes

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

The installer auto-detects the current OS and then:

- ask for the API URL
- ask for the enrollment email
- generate an enrollment token
- wait for approval from the web UI
- install the agent as a background service
- start it automatically in the background

When you run the installer from a source checkout, it also mirrors the entered values into the repository-level `config.json` for local visibility. The managed service still reads its OS-specific config path shown below.

### Ubuntu

Installed paths on Ubuntu:

- Binary: `/opt/noderax-agent/noderax-agent`
- Symlink: `/usr/local/bin/noderax-agent`
- Config: `/etc/noderax-agent/config.json`
- State: `/var/lib/noderax-agent/agent_identity.json`
- Service: `/etc/systemd/system/noderax-agent.service`

### macOS

Installed paths on macOS:

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

Leave `enrollment_token` empty. The enrollment command will populate it automatically.

### 3. Run enrollment

```bash
./noderax-agent enroll
```

The command will:

- ask for the enrollment email
- call `POST /api/v1/enrollments/initiate`
- display the short-lived token
- save the token in the config file
- wait for `GET /api/v1/enrollments/{token}` to become `approved`
- write `nodeId` and `agentToken` into the identity state file

### 4. Start the agent manually

```bash
./noderax-agent
```

If you want to point to a specific config file:

```bash
NODERAX_CONFIG_FILE=/path/to/config.json ./noderax-agent
```

## Service Management

Once installed, the same CLI is used on both Ubuntu and macOS.

```bash
sudo noderax-agent start
sudo noderax-agent stop
sudo noderax-agent restart
sudo noderax-agent status
```

## Configuration Management

Show the active managed config:

```bash
noderax-agent config show
```

Update a config value:

```bash
sudo noderax-agent config set api_url https://api.example.com
sudo noderax-agent config set task_timeout 30s
sudo noderax-agent config set metrics_interval 15s
sudo noderax-agent config set log_level debug
```

After `config set`, the managed service is restarted automatically when installed.

Supported config keys:

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

## Realtime Socket.IO v4

Required environment variables for realtime mode:

```bash
NODERAX_API_URL=https://<domain>
NODERAX_REALTIME_NAMESPACE=/agent-realtime
NODERAX_REALTIME_PATH=/socket.io/
NODERAX_REALTIME_PING_INTERVAL=2s
NODERAX_REALTIME_METRICS_INTERVAL=3s
```

Notes:

- Auth is performed after socket connection with the `agent.auth` event.
- Namespace and Engine.IO path are configured separately.
- Realtime startup performs a self-check with `GET {baseURL}/socket.io/?EIO=4&transport=polling` and expects a `sid` in response.

Common failure modes:

- `invalid URL input`:
  `NODERAX_API_URL` is malformed or missing host.
- `tls/proxy handshake failure`:
  TLS certificate/proxy setup issue between agent and API.
- `namespace connect failure`:
  wrong namespace or Socket.IO path mismatch.
- `auth error`:
  `agent.auth` payload rejected by backend.

## Enrollment Flow

The agent uses the following flow:

1. Prompt the operator for the enrollment email.
2. Send `POST /api/v1/enrollments/initiate` with:
   - `email`
   - `hostname`
   - `additionalInfo.os`
   - `additionalInfo.arch`
   - `additionalInfo.agentVersion`
3. Save the returned short-lived token into the config file.
4. Wait for approval from the web UI via `GET /api/v1/enrollments/{token}`.
5. Persist the approved `nodeId` and `agentToken`.
6. Start realtime websocket session and metrics/task execution loops.

## Task Execution & Environment

The agent executes `shell.exec` tasks in a controlled non-interactive environment:
- **PAGER=cat:** Prevents commands from hanging in interactive pagers (e.g., `git log`, `apt`).
- **COLUMNS=100000:** Ensures wide output lines are not truncated or wrapped prematurely.
- **Cancellation:** Tasks can be cancelled via the API. The agent handles cancellation gracefully, ensuring all pending logs are drained with a 3-second timeout before the execution context is destroyed.
- **Scheduled task compatibility:** Recurring commands created in the web UI are enqueued by the API as ordinary `shell.exec` tasks, so the execution path is identical to a manually queued command.

For package management (`packageList`), the agent now uses optimized `dpkg -l` parsing on Debian/Ubuntu systems to provide structured package metadata (name, version, architecture, description).

## Project Structure

- [`cmd/agent`](cmd/agent): application entrypoint and CLI dispatch
- [`internal/agent`](internal/agent): enrollment, identity persistence, and worker bootstrap
- [`internal/agentctl`](internal/agentctl): install, service management, and config CLI
- [`internal/api`](internal/api): API request/response models and HTTP client
- [`internal/tasks`](internal/tasks): realtime task execution
- [`internal/metrics`](internal/metrics): metrics worker
- [`internal/realtime`](internal/realtime): websocket realtime client and event handlers
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

Run in foreground during development:

```bash
cp config.example.json config.json
./noderax-agent enroll
./noderax-agent
```

## Notes

- Ubuntu installation assumes a `systemd`-based system.
- macOS installation assumes `launchd` and requires `sudo`.
- The managed service configuration path can be overridden with `NODERAX_CONFIG_FILE`.
- For production release packaging, generating separate Ubuntu and macOS artifacts is recommended.
