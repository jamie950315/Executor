#!/usr/bin/env bash
set -euo pipefail

STATE_DIR="${EXECUTOR_STATE_DIR:-/var/lib/executor}"
bash "$(dirname "$0")/rollback.sh"
rm -rf "${STATE_DIR}"
printf 'removed %s\n' "${STATE_DIR}"
