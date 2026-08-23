#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DASHBOARD_DIR="${ROOT_DIR}/dashboard"

if ! command -v node >/dev/null 2>&1; then
  printf 'Node is required to deploy the Unified Dashboard.\n' >&2
  exit 1
fi
if ! command -v npm >/dev/null 2>&1; then
  printf 'npm is required to deploy the Unified Dashboard.\n' >&2
  exit 1
fi
if [[ ! -f "${DASHBOARD_DIR}/scripts/deploy.mjs" ]]; then
  printf 'Dashboard source deployment payload is incomplete.\n' >&2
  exit 1
fi

exec node "${DASHBOARD_DIR}/scripts/deploy.mjs" "$@"
