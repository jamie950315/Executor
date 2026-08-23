#!/usr/bin/env bash
set -euo pipefail

EXECUTOR=""
DASHBOARD_URL=""
TOKEN_FILE=""
TEMPORARY_TOKEN=0
OWNED_COPY=""
ENROLLMENT_SUCCEEDED=0

usage() {
  printf 'Usage: %s --executor <path> --url <https-origin> --token-file <protected-file> [--temporary-token]\n' "$0"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --executor) [[ $# -ge 2 ]] || { usage >&2; exit 2; }; EXECUTOR="$2"; shift 2 ;;
    --url) [[ $# -ge 2 ]] || { usage >&2; exit 2; }; DASHBOARD_URL="$2"; shift 2 ;;
    --token-file) [[ $# -ge 2 ]] || { usage >&2; exit 2; }; TOKEN_FILE="$2"; shift 2 ;;
    --temporary-token) TEMPORARY_TOKEN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

if [[ -z "${EXECUTOR}" || -z "${DASHBOARD_URL}" || -z "${TOKEN_FILE}" || ! -x "${EXECUTOR}" ]]; then
  usage >&2
  exit 2
fi
if [[ ! "${DASHBOARD_URL}" =~ ^https://[^/?#]+/?$ ]]; then
  printf 'Unified Dashboard URL must be an HTTPS origin.\n' >&2
  exit 2
fi

protected_mode() {
  local mode=""
  if mode="$(stat -c '%a' -- "$1" 2>/dev/null)"; then
    :
  else
    mode="$(stat -f '%Lp' -- "$1" 2>/dev/null || true)"
  fi
  [[ "${mode}" == "600" ]]
}

if [[ ! -f "${TOKEN_FILE}" || -L "${TOKEN_FILE}" ]] || ! protected_mode "${TOKEN_FILE}"; then
  printf 'Dashboard enrollment token must be a regular Unix mode-0600 file.\n' >&2
  exit 1
fi

cleanup() {
  if [[ -n "${OWNED_COPY}" && -e "${OWNED_COPY}" ]]; then
    rm -f -- "${OWNED_COPY}"
  fi
}
trap cleanup EXIT

TOKEN_FOR_ENROLLMENT="${TOKEN_FILE}"
if [[ "${TEMPORARY_TOKEN}" != "1" ]]; then
  umask 077
  OWNED_COPY="$(mktemp "${TMPDIR:-/tmp}/executor-dashboard-enrollment.XXXXXX")"
  cp -- "${TOKEN_FILE}" "${OWNED_COPY}"
  chmod 0600 "${OWNED_COPY}"
  TOKEN_FOR_ENROLLMENT="${OWNED_COPY}"
fi

"${EXECUTOR}" dashboard enroll --url "${DASHBOARD_URL}" --token-file "${TOKEN_FOR_ENROLLMENT}" >/dev/null
ENROLLMENT_SUCCEEDED=1
if [[ -e "${TOKEN_FOR_ENROLLMENT}" ]]; then
  rm -f -- "${TOKEN_FOR_ENROLLMENT}"
fi
"${EXECUTOR}" dashboard status --json >/dev/null
"${EXECUTOR}" status --json >/dev/null
printf 'Executor is enrolled with Unified Dashboard %s; local services remain healthy.\n' "${DASHBOARD_URL}"
