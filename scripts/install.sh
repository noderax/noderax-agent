#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AGENT_BIN="${AGENT_BIN:-$ROOT_DIR/noderax-agent}"

case "$(uname -s)" in
  Linux|Darwin)
    ;;
  *)
    echo "Unsupported operating system: $(uname -s)" >&2
    exit 1
    ;;
esac

if [[ "${EUID}" -ne 0 ]]; then
  exec sudo -E "$0" "$@"
fi

if [[ ! -x "$AGENT_BIN" ]]; then
  if ! command -v go >/dev/null 2>&1; then
    echo "Go is not installed and no prebuilt noderax-agent binary was found." >&2
    echo "Install Go or place a compiled binary at $AGENT_BIN." >&2
    exit 1
  fi

  cd "$ROOT_DIR"
  go build -o "$AGENT_BIN" ./cmd/agent
fi

export NODERAX_CONFIG_MIRROR_FILE="${NODERAX_CONFIG_MIRROR_FILE:-$ROOT_DIR/config.json}"

"$AGENT_BIN" install "$@"

if [[ -n "${SUDO_UID:-}" && -n "${SUDO_GID:-}" && -f "$NODERAX_CONFIG_MIRROR_FILE" ]]; then
  chown "$SUDO_UID:$SUDO_GID" "$NODERAX_CONFIG_MIRROR_FILE"
fi
