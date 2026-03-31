#!/usr/bin/env bash
set -euo pipefail

SERVICE_USER="${NODERAX_AGENT_SERVICE_USER:-noderax}"
SERVICE_HOME="${NODERAX_AGENT_SERVICE_HOME:-/var/lib/noderax-agent}"
INSTALL_DIR="${NODERAX_AGENT_INSTALL_DIR:-/opt/noderax-agent}"
CONFIG_DIR="${NODERAX_AGENT_CONFIG_DIR:-/etc/noderax-agent}"
STATE_DIR="${NODERAX_AGENT_STATE_DIR:-/var/lib/noderax-agent}"
DOWNLOAD_BASE_URL="${NODERAX_AGENT_DOWNLOAD_BASE_URL:-https://cdn.noderax.net/noderax-agent/releases}"
VERSION="${NODERAX_AGENT_VERSION:-latest}"
LOG_LEVEL="${NODERAX_AGENT_LOG_LEVEL:-info}"

API_URL=""
BOOTSTRAP_TOKEN=""
BINARY_URL="${NODERAX_AGENT_BINARY_URL:-}"

usage() {
  cat <<'EOF'
Usage: install.sh --api-url <url> --bootstrap-token <token> [--log-level <level>] [--version <release>]
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --api-url)
      API_URL="${2:-}"
      shift 2
      ;;
    --bootstrap-token)
      BOOTSTRAP_TOKEN="${2:-}"
      shift 2
      ;;
    --log-level)
      LOG_LEVEL="${2:-}"
      shift 2
      ;;
    --version)
      VERSION="${2:-}"
      shift 2
      ;;
    --binary-url)
      BINARY_URL="${2:-}"
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
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y "${MISSING_PACKAGES[@]}"
fi

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

SUDOERS_FILE="/etc/sudoers.d/noderax-agent"
printf '%s ALL=(ALL) NOPASSWD:ALL\n' "${SERVICE_USER}" > "${SUDOERS_FILE}"
chmod 0440 "${SUDOERS_FILE}"
if command -v visudo >/dev/null 2>&1; then
  visudo -cf "${SUDOERS_FILE}" >/dev/null
fi

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
  BINARY_URL="${DOWNLOAD_BASE_URL}/${VERSION}/noderax-agent-linux-${ARCH}"
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
TMP_BINARY="${TMP_DIR}/noderax-agent"

curl -fsSL "${BINARY_URL}" -o "${TMP_BINARY}"
chmod 0755 "${TMP_BINARY}"

export NODERAX_CONFIG_MIRROR_FILE=""

"${TMP_BINARY}" install \
  --non-interactive \
  --api-url "${API_URL}" \
  --bootstrap-token "${BOOTSTRAP_TOKEN}" \
  --log-level "${LOG_LEVEL}"
