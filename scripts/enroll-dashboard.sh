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
if [[ ! "${WAIT_ATTEMPTS}" =~ ^[1-9][0-9]{0,8}$ ]] ||
   [[ ! "${WAIT_DELAY}" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
  printf 'Dashboard relay wait settings are invalid.\n' >&2
  exit 2
fi
if ! command -v python3 >/dev/null 2>&1; then
  printf 'Python 3 is required to validate Dashboard enrollment token paths.\n' >&2
  exit 1
fi
if ! WAIT_ATTEMPTS="${WAIT_ATTEMPTS}" WAIT_DELAY="${WAIT_DELAY}" python3 - <<'PY'
from decimal import Decimal, InvalidOperation
import os
import sys

try:
    attempts = int(os.environ["WAIT_ATTEMPTS"])
    delay = Decimal(os.environ["WAIT_DELAY"])
except (InvalidOperation, ValueError):
    raise SystemExit(1)
if attempts < 1 or attempts > 300 or not delay.is_finite() or delay <= 0 or Decimal(attempts) * delay > Decimal(300):
    raise SystemExit(1)
PY
then
  printf 'Dashboard relay wait settings must be positive and must not exceed 300 seconds total.\n' >&2
  exit 2
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

if ! ORIGINAL_TOKEN_IDENTITY="$(python3 - "${TOKEN_FILE}" <<'PY'
import os
import stat
import sys

info = os.lstat(os.path.abspath(sys.argv[1]))
if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600:
    raise SystemExit(1)
print(f"{info.st_dev}:{info.st_ino}")
PY
)" || [[ ! "${ORIGINAL_TOKEN_IDENTITY}" =~ ^[0-9]+:[0-9]+$ ]]; then
  printf 'Dashboard enrollment token file identity could not be captured safely.\n' >&2
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
  if ! python3 - "${TOKEN_FILE}" "${ORIGINAL_TOKEN_IDENTITY}" <<'PY'
import os
import platform
import secrets
import stat
import sys

path = os.path.abspath(sys.argv[1])
expected_dev, expected_ino = (int(value) for value in sys.argv[2].split(":"))
parts = path.split(os.sep)
current = os.sep
known_darwin_links = {
    "/etc": "private/etc",
    "/tmp": "private/tmp",
    "/var": "private/var",
}
for part in parts[1:]:
    current = os.path.join(current, part)
    info = os.lstat(current)
    if stat.S_ISLNK(info.st_mode):
        if platform.system() == "Darwin" and current in known_darwin_links and os.readlink(current) == known_darwin_links[current]:
            continue
        raise SystemExit(1)
    if current != path and not stat.S_ISDIR(info.st_mode):
        raise SystemExit(1)

parent = os.path.dirname(path)
name = os.path.basename(path)
parent_info = os.lstat(parent)
flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
parent_fd = os.open(parent, flags)
try:
    opened_parent = os.fstat(parent_fd)
    if (opened_parent.st_dev, opened_parent.st_ino) != (parent_info.st_dev, parent_info.st_ino):
        raise SystemExit(1)
    current_info = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
    if not stat.S_ISREG(current_info.st_mode) or stat.S_IMODE(current_info.st_mode) != 0o600:
        raise SystemExit(1)
    if (current_info.st_dev, current_info.st_ino) != (expected_dev, expected_ino):
        raise SystemExit(1)
    tombstone = f".executor-enrollment-delete-{os.getpid()}-{secrets.token_hex(8)}"
    os.rename(name, tombstone, src_dir_fd=parent_fd, dst_dir_fd=parent_fd)
    moved_info = os.stat(tombstone, dir_fd=parent_fd, follow_symlinks=False)
    if (moved_info.st_dev, moved_info.st_ino) != (expected_dev, expected_ino):
        try:
            os.rename(tombstone, name, src_dir_fd=parent_fd, dst_dir_fd=parent_fd)
        except OSError:
            pass
        raise SystemExit(1)
    os.unlink(tombstone, dir_fd=parent_fd)
finally:
    os.close(parent_fd)
PY
  then
    printf 'Designated Dashboard enrollment token was missing or replaced; refusing deletion.\n' >&2
    exit 1
  fi
fi
printf 'Executor is enrolled with Unified Dashboard %s; local services remain healthy.\n' "${DASHBOARD_URL}"
