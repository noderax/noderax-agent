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
