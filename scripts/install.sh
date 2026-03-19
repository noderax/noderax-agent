#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AGENT_BIN="${AGENT_BIN:-$ROOT_DIR/noderax-agent}"

if [[ -x "$AGENT_BIN" ]]; then
  exec "$AGENT_BIN" enroll "$@"
fi

if command -v go >/dev/null 2>&1; then
  cd "$ROOT_DIR"
  exec go run ./cmd/agent enroll "$@"
fi

echo "noderax-agent binary not found and Go is not installed." >&2
echo "Set AGENT_BIN to the compiled agent binary or install Go to run from source." >&2
exit 1
