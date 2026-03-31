#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="${ROOT_DIR}/dist/release"
AGENT_VERSION="${NODERAX_AGENT_VERSION:-dev}"
AGENT_COMMIT="${NODERAX_AGENT_COMMIT:-unknown}"
AGENT_BUILD_DATE="${NODERAX_AGENT_BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

usage() {
  cat <<'EOF'
Usage: build-release.sh [--version <version>] [--commit <sha>] [--build-date <iso8601>] [--output-dir <path>]
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      AGENT_VERSION="${2:-}"
      shift 2
      ;;
    --commit)
      AGENT_COMMIT="${2:-}"
      shift 2
      ;;
    --build-date)
      AGENT_BUILD_DATE="${2:-}"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

mkdir -p "${OUTPUT_DIR}"

build_binary() {
  local arch="$1"
  local output_path="${OUTPUT_DIR}/noderax-agent-linux-${arch}"

  GOOS=linux GOARCH="${arch}" \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${AGENT_VERSION} -X main.commit=${AGENT_COMMIT} -X main.buildDate=${AGENT_BUILD_DATE}" \
      -o "${output_path}" \
      ./cmd/agent
}

cd "${ROOT_DIR}"

build_binary amd64
build_binary arm64
cp "${ROOT_DIR}/scripts/install.sh" "${OUTPUT_DIR}/install.sh"

if command -v sha256sum >/dev/null 2>&1; then
  (
    cd "${OUTPUT_DIR}"
    sha256sum noderax-agent-linux-amd64 noderax-agent-linux-arm64 > SHA256SUMS
  )
elif command -v shasum >/dev/null 2>&1; then
  (
    cd "${OUTPUT_DIR}"
    shasum -a 256 noderax-agent-linux-amd64 noderax-agent-linux-arm64 > SHA256SUMS
  )
else
  echo "Neither sha256sum nor shasum is available to generate checksums." >&2
  exit 1
fi

echo "Release assets written to ${OUTPUT_DIR}"
