#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
PREPARE_ONLY=""

usage() {
  printf 'Usage: %s [--prepare-only <empty-directory>]\n' "$0"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prepare-only)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      PREPARE_ONLY="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

export PATH="/opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin:${PATH}"

for command_name in go tar; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    printf 'required command is not installed or not in PATH: %s\n' "${command_name}" >&2
    exit 1
  fi
done

kernel="$(uname -s)"
machine="$(uname -m)"
case "${machine}" in
  x86_64|amd64) goarch="amd64" ;;
  arm64|aarch64) goarch="arm64" ;;
  *) printf 'unsupported host architecture: %s\n' "${machine}" >&2; exit 1 ;;
esac

case "${kernel}" in
  Darwin)
    goos="darwin"
    executor_target="macos"
    archive_extension="tar.gz"
    ;;
  Linux)
    goos="linux"
    executor_target="linux"
    if [[ -n "${WSL_INTEROP:-}" || -n "${WSL_DISTRO_NAME:-}" ]] || grep -Eqi '(microsoft|wsl)' /proc/sys/kernel/osrelease 2>/dev/null; then
      executor_target="wsl"
    fi
    archive_extension="tar.gz"
    ;;
  *)
    printf 'unsupported host operating system: %s\n' "${kernel}" >&2
    exit 1
    ;;
esac

if [[ -z "${PREPARE_ONLY}" ]]; then
  : "${EXECUTOR_DOMAIN:?set EXECUTOR_DOMAIN to the public hostname}"
  for command_name in cloudflared python3; do
    if ! command -v "${command_name}" >/dev/null 2>&1; then
      printf 'required command is not installed or not in PATH: %s\n' "${command_name}" >&2
      exit 1
    fi
  done
  if [[ "$(id -u)" != "0" ]]; then
    printf 'installation requires administrator privileges; run with sudo -E so the deployment variables are preserved\n' >&2
    exit 1
  fi
fi

WORK_DIR=""
cleanup() {
  if [[ -n "${WORK_DIR}" && -d "${WORK_DIR}" ]]; then
    rm -rf -- "${WORK_DIR}"
  fi
}
trap cleanup EXIT

if [[ -n "${PREPARE_ONLY}" ]]; then
  BUNDLE_DIR="${PREPARE_ONLY}"
  mkdir -p "${BUNDLE_DIR}"
  if [[ -n "$(find "${BUNDLE_DIR}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    printf 'prepare-only directory must be empty: %s\n' "${BUNDLE_DIR}" >&2
    exit 1
  fi
  BUNDLE_DIR="$(cd "${BUNDLE_DIR}" && pwd)"
  RELEASE_DIR="$(mktemp -d)"
  WORK_DIR="${RELEASE_DIR}"
else
  WORK_DIR="$(mktemp -d)"
  RELEASE_DIR="${WORK_DIR}/release"
  BUNDLE_DIR="${WORK_DIR}/bundle"
  mkdir -p "${BUNDLE_DIR}"
fi

(
  cd "${ROOT_DIR}"
  OUT_DIR="${RELEASE_DIR}" \
  EXECUTOR_BUILD_TARGETS="${goos}/${goarch}" \
  EXECUTOR_MACOS_SIGN_IDENTITY="${EXECUTOR_MACOS_SIGN_IDENTITY:--}" \
    bash "${SCRIPT_DIR}/build-release-artifacts.sh"
)

archive="${RELEASE_DIR}/executor_${goos}_${goarch}.${archive_extension}"
if [[ ! -f "${archive}" ]]; then
  printf 'native release archive was not created: %s\n' "${archive}" >&2
  exit 1
fi
tar -xzf "${archive}" -C "${BUNDLE_DIR}"

if [[ -n "${PREPARE_ONLY}" ]]; then
  printf 'deployable Executor bundle prepared at %s\n' "${BUNDLE_DIR}"
  exit 0
fi

export EXECUTOR_TARGET="${executor_target}"
export EXECUTOR_BUNDLE_ROOT="${BUNDLE_DIR}"
bash "${BUNDLE_DIR}/scripts/bootstrap.sh"

installed_executor="${EXECUTOR_INSTALL_BINARY_PATH:-/usr/local/bin/executor}"
"${installed_executor}" status
"${installed_executor}" doctor --full
