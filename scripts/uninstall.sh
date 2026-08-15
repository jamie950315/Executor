#!/usr/bin/env bash
set -euo pipefail

STATE_DIR="${EXECUTOR_STATE_DIR:-executor-state}"
bash "$(dirname "$0")/rollback.sh"
rm -rf "${STATE_DIR}"
printf 'removed %s\n' "${STATE_DIR}"
