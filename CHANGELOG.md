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
- Platform-wide `Updates` center for official agent release visibility, rollout
  selection, and changelog browsing.
- Sequential fleet rollouts with retry, skip, resume, cancel, rollback, and
  heartbeat-confirmed completion.
- Detached self-update command for Linux `amd64` and `arm64` agents using
  official CDN metadata with GitHub Releases fallback.

### Security
- Self-update sudo access is restricted to the dedicated `noderax-agent update`
  command instead of generic root shell execution.

## [1.0.0] - 2026-03-01

### Added
- One-click bootstrap installer for Ubuntu and Debian hosts.
- Background service management, telemetry, task polling, and package
  operations for enrolled nodes.
- Interactive terminal support over the agent realtime channel.

### Security
- Dedicated `noderax` runtime user with least-privilege task execution defaults.
