#!/usr/bin/env bash
set -eEuo pipefail

normalize_value() {
  local value="${1:-}"

  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"

  if [[ "${value}" == \"*\" && "${value}" == *\" ]]; then
    value="${value:1:${#value}-2}"
  elif [[ "${value}" == \'*\' && "${value}" == *\' ]]; then
    value="${value:1:${#value}-2}"
  fi

  printf '%s' "${value}"
}

SERVICE_USER="$(normalize_value "${NODERAX_AGENT_SERVICE_USER:-noderax}")"
SERVICE_HOME="$(normalize_value "${NODERAX_AGENT_SERVICE_HOME:-/var/lib/noderax-agent}")"
INSTALL_DIR="$(normalize_value "${NODERAX_AGENT_INSTALL_DIR:-/opt/noderax-agent}")"
CONFIG_DIR="$(normalize_value "${NODERAX_AGENT_CONFIG_DIR:-/etc/noderax-agent}")"
STATE_DIR="$(normalize_value "${NODERAX_AGENT_STATE_DIR:-/var/lib/noderax-agent}")"
SYMLINK_PATH="/usr/local/bin/noderax-agent"
DOWNLOAD_BASE_URL="$(normalize_value "${NODERAX_AGENT_DOWNLOAD_BASE_URL:-https://cdn.noderax.net/noderax-agent/releases}")"
AGENT_VERSION="$(normalize_value "${NODERAX_AGENT_VERSION:-latest}")"
LOG_LEVEL="$(normalize_value "${NODERAX_AGENT_LOG_LEVEL:-info}")"

API_URL=""
BOOTSTRAP_TOKEN=""
BINARY_URL="$(normalize_value "${NODERAX_AGENT_BINARY_URL:-}")"
CURRENT_PROGRESS=0
CURRENT_STATUS="installing"
CURRENT_STAGE="command_generated"
CURRENT_MESSAGE="Installer is waiting to start."

usage() {
  cat <<'EOF'
Usage: install.sh --api-url <url> --bootstrap-token <token> [--log-level <level>] [--version <release>]
EOF
}

normalize_api_origin() {
  local value="${1:-}"

  value="$(normalize_value "${value}")"
  value="${value%/}"
  value="${value%/api/v1}"
  value="${value%/v1}"

  printf '%s' "${value}"
}

json_escape() {
  local value="${1:-}"

  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\n'/\\n}"
  value="${value//$'\r'/\\r}"
  value="${value//$'\t'/\\t}"

  printf '%s' "${value}"
}

report_progress() {
  local stage="${1:-}"
  local progress="${2:-0}"
  local status="${3:-installing}"
  local message="${4:-}"
  local endpoint payload

  stage="$(normalize_value "${stage}")"
  progress="$(normalize_value "${progress}")"
  status="$(normalize_value "${status}")"
  message="$(normalize_value "${message}")"

  [[ -z "${API_URL}" || -z "${BOOTSTRAP_TOKEN}" || -z "${stage}" ]] && return 0

  CURRENT_STAGE="${stage}"
  CURRENT_PROGRESS="${progress:-0}"
  CURRENT_STATUS="${status:-installing}"
  CURRENT_MESSAGE="${message}"

  endpoint="$(normalize_api_origin "${API_URL}")/api/v1/node-installs/progress"
  payload=$(
    printf '{"token":"%s","stage":"%s","status":"%s","progressPercent":%s,"statusMessage":"%s"}' \
      "$(json_escape "${BOOTSTRAP_TOKEN}")" \
      "$(json_escape "${CURRENT_STAGE}")" \
      "$(json_escape "${CURRENT_STATUS}")" \
      "${CURRENT_PROGRESS:-0}" \
      "$(json_escape "${CURRENT_MESSAGE}")"
  )

  curl -fsS \
    -H "Content-Type: application/json" \
    -X POST \
    "${endpoint}" \
    -d "${payload}" >/dev/null || true
}

on_error() {
  local exit_code=$?

  if [[ "${exit_code}" -ne 0 ]]; then
    report_progress \
      "failed" \
      "${CURRENT_PROGRESS:-0}" \
      "failed" \
      "${CURRENT_MESSAGE:-Installer failed before it could complete.}"
  fi

  exit "${exit_code}"
}

trap on_error ERR

while [[ $# -gt 0 ]]; do
  case "$1" in
    --api-url)
      API_URL="${2:-}"
      API_URL="$(normalize_value "${API_URL}")"
      shift 2
      ;;
    --bootstrap-token)
      BOOTSTRAP_TOKEN="${2:-}"
      BOOTSTRAP_TOKEN="$(normalize_value "${BOOTSTRAP_TOKEN}")"
      shift 2
      ;;
    --log-level)
      LOG_LEVEL="${2:-}"
      LOG_LEVEL="$(normalize_value "${LOG_LEVEL}")"
      shift 2
      ;;
    --version)
      AGENT_VERSION="${2:-}"
      AGENT_VERSION="$(normalize_value "${AGENT_VERSION}")"
      shift 2
      ;;
    --binary-url)
      BINARY_URL="${2:-}"
      BINARY_URL="$(normalize_value "${BINARY_URL}")"
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

if [[ -z "${API_URL}" || -z "${BOOTSTRAP_TOKEN}" ]]; then
  usage >&2
  exit 1
fi

API_URL="$(normalize_api_origin "${API_URL}")"

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run this installer as root. Example: curl -fsSL https://cdn.noderax.net/noderax-agent/install.sh | sudo bash -s -- --api-url ${API_URL} --bootstrap-token <token>" >&2
  exit 1
fi

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "Only Linux hosts are supported by this installer." >&2
  exit 1
fi

if [[ ! -r /etc/os-release ]]; then
  echo "Unable to detect Linux distribution." >&2
  exit 1
fi

# shellcheck disable=SC1091
source /etc/os-release
DISTRO_FAMILY="${ID_LIKE:-} ${ID:-}"
if [[ "${DISTRO_FAMILY}" != *debian* && "${DISTRO_FAMILY}" != *ubuntu* ]]; then
  echo "This installer currently supports Ubuntu and Debian only." >&2
  exit 1
fi

if ! command -v apt-get >/dev/null 2>&1; then
  echo "apt-get is required on the target host." >&2
  exit 1
fi

report_progress \
  "installer_started" \
  8 \
  "installing" \
  "Installer started on the target server."

MISSING_PACKAGES=()
if ! command -v curl >/dev/null 2>&1; then
  MISSING_PACKAGES+=("curl")
fi
if ! command -v sudo >/dev/null 2>&1; then
  MISSING_PACKAGES+=("sudo")
fi
if ! dpkg -s ca-certificates >/dev/null 2>&1; then
  MISSING_PACKAGES+=("ca-certificates")
fi

if [[ "${#MISSING_PACKAGES[@]}" -gt 0 ]]; then
  report_progress \
    "dependencies_installing" \
    18 \
    "installing" \
    "Installing required operating system packages."
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y "${MISSING_PACKAGES[@]}"
fi

report_progress \
  "dependencies_ready" \
  30 \
  "installing" \
  "Required operating system packages are ready."

if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemd is required on the target host." >&2
  exit 1
fi

if ! getent group "${SERVICE_USER}" >/dev/null 2>&1; then
  groupadd --system "${SERVICE_USER}"
fi

if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
  useradd \
    --system \
    --gid "${SERVICE_USER}" \
    --home-dir "${SERVICE_HOME}" \
    --create-home \
    --shell /usr/sbin/nologin \
    "${SERVICE_USER}"
fi

mkdir -p "${INSTALL_DIR}" "${CONFIG_DIR}" "${STATE_DIR}"
chown -R "${SERVICE_USER}:${SERVICE_USER}" "${INSTALL_DIR}" "${CONFIG_DIR}" "${STATE_DIR}"
chmod 0755 "${INSTALL_DIR}" "${CONFIG_DIR}" "${STATE_DIR}"

report_progress \
  "service_user_ready" \
  45 \
  "installing" \
  "Preparing the noderax service account and runtime directories."

ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64|amd64)
    ARCH="amd64"
    ;;
  aarch64|arm64)
    ARCH="arm64"
    ;;
  *)
    echo "Unsupported CPU architecture: ${ARCH}" >&2
    exit 1
    ;;
esac

if [[ -z "${BINARY_URL}" ]]; then
  BINARY_URL="${DOWNLOAD_BASE_URL}/${AGENT_VERSION}/noderax-agent-linux-${ARCH}"
fi

if [[ ! "${BINARY_URL}" =~ ^https?://[^[:space:]]+$ ]]; then
  echo "Computed agent binary URL is invalid: ${BINARY_URL}" >&2
  echo "Check NODERAX_AGENT_BINARY_URL, NODERAX_AGENT_DOWNLOAD_BASE_URL, and NODERAX_AGENT_VERSION." >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
TMP_BINARY="${TMP_DIR}/noderax-agent"

report_progress \
  "binary_download_started" \
  60 \
  "installing" \
  "Downloading the Noderax agent binary."

echo "Downloading Noderax Agent binary from ${BINARY_URL}"
curl -fsSL "${BINARY_URL}" -o "${TMP_BINARY}"
chmod 0755 "${TMP_BINARY}"

report_progress \
  "binary_downloaded" \
  74 \
  "installing" \
  "Agent binary downloaded. Bootstrapping node credentials next."

export NODERAX_CONFIG_MIRROR_FILE=""

report_progress \
  "agent_bootstrapping" \
  88 \
  "installing" \
  "Bootstrapping node credentials and writing service config."

"${TMP_BINARY}" install \
  --non-interactive \
  --api-url "${API_URL}" \
  --bootstrap-token "${BOOTSTRAP_TOKEN}" \
  --log-level "${LOG_LEVEL}"

report_progress \
  "service_started" \
  100 \
  "completed" \
  "Agent installed successfully and the noderax service is running."
