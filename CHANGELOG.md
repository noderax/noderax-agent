# Changelog

`CHANGELOG.md` is the canonical source for official Noderax Agent release notes.
Every tagged release that should appear in the platform `Updates` center must
have a matching section in this file before the `agent-v<version>` tag is
published.

Formatting rules:

- Use `## [<version>] - YYYY-MM-DD` for each tagged release.
- Use `### <Section>` headings such as `Added`, `Changed`, `Fixed`, or `Security`.
- Use flat `-` bullet items under each section.
- Keep notes operator-facing because the API, web UI, CDN manifest, and GitHub
  Release body are generated from this file.

## [Unreleased]

## [2026.5.1] - 2026-05-14

### Added

- Added cloud metadata location detection for AWS, GCP, and Azure so agents can report provider, region, and zone when the host metadata service is available.
- Added node location reporting to interactive enrollment, bootstrap enrollment, and realtime authentication payloads so the control plane can keep node location metadata current.

## [1.0.7] - 2026-04-08

### Added

- Added support for custom API TLS trust roots through `api_tls_ca_file` in config files and `NODERAX_API_TLS_CA_FILE` / `API_TLS_CA_FILE` environment overrides.

### Changed

- Changed API client construction to initialize with system CA roots plus optional custom CA bundle loading, and enforced TLS `minVersion` at TLS 1.2 for outbound API requests.
- Changed enrollment and managed update code paths to use error-returning API client initialization so TLS CA configuration issues are surfaced before network operations start.

### Fixed

- Fixed startup, install, bootstrap, and managed update flows to fail fast with explicit `configure API client` errors when API TLS CA files are unreadable or invalid.

## [1.0.6] - 2026-04-05

### Added

- Added a new `log.scan` task type with payload validation for `mode` and `sourcePresetId`, including optional root execution guarded by `task` scope checks.
- Added `noderax-agent log-scan --request <path>` to execute log scan requests from a JSON file and return structured JSON results for task parsing.
- Added a dedicated log scanning engine that supports preset sources (`syslog`, `auth.log`, `kern.log`, `noderax-agent`) with `preview` and `monitor` modes, source-aware cursor handling, and hard limits for lines, bytes, and backfill.

### Fixed

- Task lifecycle log shipping now truncates oversized log lines to the API-safe limit and retries queued-state conflicts before failing, reducing cases where tasks appear stuck in `queued` without visible progress.
- Root `log.scan` execution now normalizes legacy `task` scope requests to `operational` scope and uses a dedicated operational helper path, so log scan operations no longer depend on task-root grants.
- Agents now re-apply an already-selected root access profile when older persisted state lacks the latest revision marker, allowing updated sudoers rules for operational log scan helpers to self-heal after upgrade.
- Monitor-mode file scans now detect log rotation (inode change) and truncation (offset beyond file size), automatically reset the cursor, replay tail lines, and emit warning metadata for downstream diagnostics.
- Log scan task result parsing now reports explicit system errors when command output is empty or invalid JSON, improving failure visibility in task logs.

## [1.0.5] - 2026-04-05

### Fixed

- Operational root panel actions now stay locked until the agent reports the profile as applied, preventing package install/remove, `apt-get update`, restart, and reboot requests from being queued while sync is still pending or failed.
- Package purge requests now queue the dedicated `packagePurge` task type instead of overloading `packageRemove`, so purge behavior stays consistent from the API through agent execution and task history.
- Linux base sudoers rules now explicitly allow `apply operational_task`, `apply operational_terminal`, and `apply task_terminal`, allowing composite root profiles to reconcile correctly on hosts with strict sudo argument matching.
- Root access profile changes now push to connected agents immediately and refresh the reported applied state without waiting for the next long-poll cycle, so profile switches no longer appear stuck after being changed in the panel.
- Root terminal sessions now use the same supported shell allowlist as the generated sudoers rules, preventing `sudo-rs: I'm sorry noderax. I'm afraid I can't do that` failures when `terminal` root access is applied.
- Managed self-update now reapplies the persisted root access profile after refreshing helpers and sudoers files, so nodes already set to `terminal` pick up the corrected terminal root rules immediately after updating.

## [1.0.4] - 2026-04-04

### Fixed

- Interactive enrollment now includes `platformVersion` and `kernelVersion` in `additionalInfo` so approved nodes can report these values to the platform instead of appearing as `Unknown` in node detail views.
- Realtime `agent.auth` now includes `platformVersion` and `kernelVersion`, allowing the control plane to refresh node platform/kernel metadata even when nodes rely on realtime reconnects instead of enrollment refresh.
- Managed self-update now refreshes the Linux root-profile helper and base sudoers file during binary replacement, preventing nodes from getting stuck with `root profile helper is not installed` after an update.
- Base sudoers rules now list explicit root-profile helper commands (`apply off|operational|task|terminal|all`) for better compatibility with `sudo-rs` argument matching.
- Linux package mutations now use a dedicated privileged helper and request-file handoff so `install`, `remove`, `purge`, and operational `apt-get update` continue working on hosts that enforce strict `sudo-rs` argument matching.
- Root task execution now uses a dedicated task-root helper handoff path, avoiding broad wildcard sudo command patterns that fail on `sudo-rs` deployments.
- Root access profile handling now supports composite profile combinations (`operational_task`, `operational_terminal`, `task_terminal`) so mixed capability sets are applied and validated consistently.

## [1.0.3] - 2026-04-04

### Added

- Added API-synced root access profile management on the agent with five profiles: `off`, `operational`, `task`, `terminal`, and `all`.
- Added persisted root-access reconciliation state (`appliedProfile`, `lastAppliedAt`, `lastError`) so the agent can report applied status and recover cleanly across restarts.
- Added root-access sync fields to control-plane contracts used by the agent (`agent.auth`, `agent.auth.ack`, and HTTP task claim request/response) so desired profile snapshots are delivered even when no task is returned.
- Added a dedicated Linux root-profile helper (`/usr/local/libexec/noderax-agent-root-profile`) that renders profile-specific sudo rules in `/etc/sudoers.d/noderax-agent-root-access`.
- Added root terminal start support via realtime `runAsRoot` flags with runtime checks that only allow root sessions for `terminal` or `all`.

### Changed

- Changed installer and `agentctl install` privilege setup to a helper-based model: static sudoers now only grants access to the self-update helper and root-profile helper.
- Changed default host posture to root access `off` at install time by applying the profile immediately during setup.
- Extended `shell.exec` payload handling with `runAsRoot` and `rootScope` (`task` | `operational`) and enforced scope checks against the currently applied profile.
- Restricted operational root execution to curated commands (`apt-get update`, `reboot`, and `systemctl restart noderax-agent`) instead of generic elevated shell execution.

### Security

- Removed legacy default passwordless package-mutation sudo grants from bootstrap/install flow and replaced them with API-driven profile reconciliation.

## [1.0.2] - 2026-04-02

### Added

- Added `noderax-agent version` and `noderax-agent --version` output so operators can quickly verify the running build metadata during fleet update tests.

## [1.0.1] - 2026-04-02

### Fixed

- Fleet self-update now hands off through a request-file based privileged helper so rollout updates work on hosts that ship `sudo-rs`, where sudoers wildcard argument matching is more restrictive than classic `sudo`.
- Managed self-update now refreshes the installed privileged helper during binary replacement so later rollouts keep using the corrected handoff path.

## [1.0.0] - 2026-04-02

### Added

- One-click bootstrap installer for Ubuntu and Debian hosts.
- Background service management, telemetry, task polling, and package
  operations for enrolled nodes.
- Interactive terminal support over the agent realtime channel.
- Platform-wide `Updates` center for official agent release visibility, rollout
  selection, and changelog browsing.
- Sequential fleet rollouts with retry, skip, resume, cancel, rollback, and
  heartbeat-confirmed completion.
- Detached self-update command for Linux `amd64` and `arm64` agents using
  official CDN metadata with GitHub Releases fallback.

### Security

- Dedicated `noderax` runtime user with least-privilege task execution defaults.
- Self-update sudo access is restricted to the dedicated `noderax-agent update`
  command instead of generic root shell execution.
