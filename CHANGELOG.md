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

## [1.0.4] - 2026-04-04

### Fixed

- Interactive enrollment now includes `platformVersion` and `kernelVersion` in `additionalInfo` so approved nodes can report these values to the platform instead of appearing as `Unknown` in node detail views.

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
