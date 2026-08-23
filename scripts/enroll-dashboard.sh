#!/usr/bin/env bash
set -euo pipefail

EXECUTOR=""
DASHBOARD_URL=""
TOKEN_FILE=""
TEMPORARY_TOKEN=0
OWNED_COPY=""
WAIT_ATTEMPTS="${EXECUTOR_DASHBOARD_WAIT_ATTEMPTS:-30}"
WAIT_DELAY="${EXECUTOR_DASHBOARD_WAIT_DELAY:-1}"

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
if [[ ! "${WAIT_ATTEMPTS}" =~ ^[1-9][0-9]*$ || "${WAIT_ATTEMPTS}" -gt 300 ]] ||
   [[ ! "${WAIT_DELAY}" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
  printf 'Dashboard relay wait settings are invalid.\n' >&2
  exit 2
fi
if ! command -v python3 >/dev/null 2>&1; then
  printf 'Python 3 is required to validate Dashboard enrollment token paths.\n' >&2
  exit 1
fi

if ! python3 - "${TOKEN_FILE}" <<'PY'
import os
import platform
import stat
import sys

path = os.path.abspath(sys.argv[1])
parts = path.split(os.sep)
current = os.sep
known_darwin_links = {
    "/etc": "private/etc",
    "/tmp": "private/tmp",
    "/var": "private/var",
}
for part in parts[1:]:
    current = os.path.join(current, part)
    try:
        info = os.lstat(current)
    except FileNotFoundError:
        continue
    if stat.S_ISLNK(info.st_mode):
        if platform.system() == "Darwin" and current in known_darwin_links and os.readlink(current) == known_darwin_links[current]:
            continue
        raise SystemExit(1)
raise SystemExit(0)
PY
then
  printf 'Dashboard enrollment token path has an unsafe ancestor.\n' >&2
  exit 1
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
umask 077
OWNED_COPY="$(mktemp "${TMPDIR:-/tmp}/executor-dashboard-enrollment.XXXXXX")"
cp -- "${TOKEN_FILE}" "${OWNED_COPY}"
chmod 0600 "${OWNED_COPY}"
TOKEN_FOR_ENROLLMENT="${OWNED_COPY}"

"${EXECUTOR}" dashboard enroll --url "${DASHBOARD_URL}" --token-file "${TOKEN_FOR_ENROLLMENT}" >/dev/null
relay_ready=0
for ((attempt = 1; attempt <= WAIT_ATTEMPTS; attempt += 1)); do
  dashboard_status=""
  local_status=""
  if dashboard_status="$("${EXECUTOR}" dashboard status --json 2>/dev/null)" &&
     local_status="$("${EXECUTOR}" status --json 2>/dev/null)" &&
     DASHBOARD_STATUS="${dashboard_status}" LOCAL_STATUS="${local_status}" python3 -c '
import json, os, sys
try:
    dashboard = json.loads(os.environ["DASHBOARD_STATUS"])
    local = json.loads(os.environ["LOCAL_STATUS"])
    ready = dashboard.get("enrolled") is True and dashboard.get("relay") == "connected" and local.get("state") == "armed" and local.get("agent") == "online" and local.get("broker") == "online"
except Exception:
    ready = False
sys.exit(0 if ready else 1)
'; then
    relay_ready=1
    break
  fi
  if [[ "${attempt}" -lt "${WAIT_ATTEMPTS}" ]]; then
    sleep "${WAIT_DELAY}"
  fi
done
if [[ "${relay_ready}" != "1" ]]; then
  printf 'Unified Dashboard relay did not become connected while local services were healthy.\n' >&2
  exit 1
fi
if [[ "${TEMPORARY_TOKEN}" == "1" ]]; then
  rm -f -- "${TOKEN_FILE}"
fi
printf 'Executor is enrolled with Unified Dashboard %s; local services remain healthy.\n' "${DASHBOARD_URL}"
