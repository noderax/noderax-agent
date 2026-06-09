#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="${ROOT_DIR}/dist/release"
AGENT_VERSION="${NODERAX_AGENT_VERSION:-dev}"
AGENT_COMMIT="${NODERAX_AGENT_COMMIT:-unknown}"
AGENT_BUILD_DATE="${NODERAX_AGENT_BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
RELEASE_BASE_URL="${NODERAX_AGENT_RELEASE_BASE_URL:-https://cdn.noderax.net/noderax-agent/releases}"
RELEASE_ARTIFACT_CHANNEL="${NODERAX_AGENT_RELEASE_ARTIFACT_CHANNEL:-}"
MINISIGN_SECRET_KEY_FILE="${NODERAX_AGENT_MINISIGN_SECRET_KEY_FILE:-}"

usage() {
  cat <<'EOF'
Usage: build-release.sh [--version <version>] [--commit <sha>] [--build-date <iso8601>] [--output-dir <path>] [--release-base-url <url>] [--release-artifact-channel <channel>] [--minisign-secret-key-file <path>]
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
    --release-base-url)
      RELEASE_BASE_URL="${2:-}"
      shift 2
      ;;
    --release-artifact-channel)
      RELEASE_ARTIFACT_CHANNEL="${2:-}"
      shift 2
      ;;
    --minisign-secret-key-file)
      MINISIGN_SECRET_KEY_FILE="${2:-}"
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

if [[ -z "${RELEASE_ARTIFACT_CHANNEL}" ]]; then
  RELEASE_ARTIFACT_CHANNEL="${AGENT_VERSION}"
fi

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
    sha256sum noderax-agent-linux-amd64 noderax-agent-linux-arm64 install.sh > SHA256SUMS
  )
elif command -v shasum >/dev/null 2>&1; then
  (
    cd "${OUTPUT_DIR}"
    shasum -a 256 noderax-agent-linux-amd64 noderax-agent-linux-arm64 install.sh > SHA256SUMS
  )
else
  echo "Neither sha256sum nor shasum is available to generate checksums." >&2
  exit 1
fi

if ! command -v minisign >/dev/null 2>&1; then
  echo "minisign is required to sign release-manifest.json." >&2
  exit 1
fi

if [[ -z "${MINISIGN_SECRET_KEY_FILE}" || ! -f "${MINISIGN_SECRET_KEY_FILE}" ]]; then
  echo "NODERAX_AGENT_MINISIGN_SECRET_KEY_FILE or --minisign-secret-key-file must point to a minisign secret key." >&2
  exit 1
fi

sha_for() {
  local name="$1"
  awk -v target="${name}" '$2 == target {print $1}' "${OUTPUT_DIR}/SHA256SUMS"
}

release_url_for() {
  local name="$1"
  printf '%s/%s/%s' "${RELEASE_BASE_URL%/}" "${RELEASE_ARTIFACT_CHANNEL}" "${name}"
}

cat > "${OUTPUT_DIR}/release-manifest.json" <<EOF
{
  "version": "${AGENT_VERSION}",
  "publishedAt": "${AGENT_BUILD_DATE}",
  "commit": "${AGENT_COMMIT}",
  "channel": "stable",
  "installer": {
    "url": "$(release_url_for install.sh)",
    "sha256": "$(sha_for install.sh)"
  },
  "artifacts": {
    "amd64": {
      "binaryUrl": "$(release_url_for noderax-agent-linux-amd64)",
      "sha256": "$(sha_for noderax-agent-linux-amd64)"
    },
    "arm64": {
      "binaryUrl": "$(release_url_for noderax-agent-linux-arm64)",
      "sha256": "$(sha_for noderax-agent-linux-arm64)"
    }
  }
}
EOF

minisign -Sm "${OUTPUT_DIR}/release-manifest.json" \
  -s "${MINISIGN_SECRET_KEY_FILE}" \
  -x "${OUTPUT_DIR}/release-manifest.json.minisig"

echo "Release assets written to ${OUTPUT_DIR}"
