#!/usr/bin/env bash
set -euo pipefail

STATE_DIR="${EXECUTOR_STATE_DIR:-executor-state}"
TARGET="${EXECUTOR_TARGET:-$(uname | tr '[:upper:]' '[:lower:]')}"
DOMAIN="${EXECUTOR_DOMAIN:?set EXECUTOR_DOMAIN}"
EXECUTOR_BIN="${EXECUTOR_BIN:-executor}"
CONFIG_PATH="${EXECUTOR_CONFIG_PATH:-${STATE_DIR}/config.json}"
DATA_DIR="${EXECUTOR_DATA_DIR:-${STATE_DIR}/data}"
LOG_PATH="${EXECUTOR_LOG_PATH:-${STATE_DIR}/executor.log}"
CLOUDFLARED_BIN="${CLOUDFLARED_BIN:-cloudflared}"
CLOUDFLARED_TOKEN_PATH="${CLOUDFLARED_TOKEN_PATH:-${STATE_DIR}/cloudflared/executor.token}"
CLOUDFLARED_LOG_PATH="${CLOUDFLARED_LOG_PATH:-${STATE_DIR}/cloudflared/cloudflared.log}"
INSTALL_BIN="${INSTALL_BIN:-install}"
SYSTEMCTL_BIN="${SYSTEMCTL_BIN:-systemctl}"
LAUNCHCTL_BIN="${LAUNCHCTL_BIN:-launchctl}"
ID_BIN="${ID_BIN:-id}"
STAT_BIN="${STAT_BIN:-stat}"
RUNUSER_BIN="${RUNUSER_BIN:-runuser}"
USERADD_BIN="${USERADD_BIN:-useradd}"
USERDEL_BIN="${USERDEL_BIN:-userdel}"
DSCL_BIN="${DSCL_BIN:-dscl}"
SYSADMINCTL_BIN="${SYSADMINCTL_BIN:-sysadminctl}"
AGENT_USER="${EXECUTOR_AGENT_USER:-executor-agent}"
AGENT_GROUP="${EXECUTOR_AGENT_GROUP:-executor-agent}"
BROKER_USER="${EXECUTOR_BROKER_USER:-root}"
BROKER_GROUP="${EXECUTOR_BROKER_GROUP:-root}"
WINDOWS_AGENT_SERVICE="${EXECUTOR_WINDOWS_AGENT_SERVICE:-NT SERVICE\ExecutorAgent}"
MANIFEST_PATH="${STATE_DIR}/service-manifest.txt"
BACKUP_ROOT="${STATE_DIR}/service-backups"
MKTEMP_BIN="${MKTEMP_BIN:-mktemp}"
TMP_BUNDLE=""
CLOUDFLARE_API_TOKEN_FILE="${CLOUDFLARE_API_TOKEN_FILE:-${EXECUTOR_CLOUDFLARE_TOKEN_FILE:-}}"
CLOUDFLARE_ACCOUNT_ID="${CLOUDFLARE_ACCOUNT_ID:-${EXECUTOR_CLOUDFLARE_ACCOUNT_ID:-}}"
CLOUDFLARE_ZONE_ID="${CLOUDFLARE_ZONE_ID:-${EXECUTOR_CLOUDFLARE_ZONE_ID:-}}"
CLOUDFLARE_TUNNEL_NAME="${CLOUDFLARE_TUNNEL_NAME:-${EXECUTOR_CLOUDFLARE_TUNNEL_NAME:-}}"

case "${TARGET}" in
  darwin) TARGET="macos" ;;
  linux) TARGET="linux" ;;
esac

service_root() {
  case "${TARGET}" in
    macos) printf '%s\n' "${EXECUTOR_INSTALL_ROOT:-/Library}" ;;
    linux|wsl) printf '%s\n' "${EXECUTOR_INSTALL_ROOT:-/etc}" ;;
    *) printf 'unsupported EXECUTOR_TARGET: %s\n' "${TARGET}" >&2; exit 1 ;;
  esac
}

ensure_linux_identity() {
  if ! "${ID_BIN}" -u "${AGENT_USER}" >/dev/null 2>&1; then
    "${USERADD_BIN}" --system --user-group "${AGENT_USER}"
    record_manifest "identity:user" "${AGENT_USER}" "delete"
  fi
}

ensure_macos_identity() {
  if ! "${DSCL_BIN}" . -read "/Users/${AGENT_USER}" >/dev/null 2>&1; then
    "${SYSADMINCTL_BIN}" -addUser "${AGENT_USER}" -shell /usr/bin/false -password "-"
    record_manifest "identity:user" "${AGENT_USER}" "delete"
  fi
}

cleanup() {
  if [[ -n "${TMP_BUNDLE}" && -d "${TMP_BUNDLE}" ]]; then
    rm -rf "${TMP_BUNDLE}"
  fi
}

trap cleanup EXIT

record_manifest() {
  printf '%s|%s|%s\n' "$1" "$2" "$3" >> "${MANIFEST_PATH}"
}

install_managed_file() {
  src="$1"
  dst="$2"
  label="$3"
  mkdir -p "$(dirname "${dst}")"
  backup="${BACKUP_ROOT}${dst}"
  if [[ -f "${dst}" ]]; then
    mkdir -p "$(dirname "${backup}")"
    cp "${dst}" "${backup}"
    mode="restore"
  else
    mode="remove"
  fi
  cp "${src}" "${dst}"
  record_manifest "${label}" "${dst}" "${mode}"
}

desktop_user_linux() {
  if [[ -n "${EXECUTOR_DESKTOP_USER:-}" ]]; then
    printf '%s\n' "${EXECUTOR_DESKTOP_USER}"
    return 0
  fi
  if [[ -n "${SUDO_USER:-}" ]]; then
    printf '%s\n' "${SUDO_USER}"
    return 0
  fi
  current_uid="$(${ID_BIN} -u)"
  if [[ "${current_uid}" != "0" ]]; then
    "${ID_BIN}" -un
    return 0
  fi
  return 1
}

run_desktop_systemctl() {
  desktop_user="$(desktop_user_linux)" || {
    printf 'unable to determine desktop user for systemctl --user\n' >&2
    exit 1
  }
  desktop_uid="$(${ID_BIN} -u "${desktop_user}")"
  runtime_dir="/run/user/${desktop_uid}"
  current_uid="$(${ID_BIN} -u)"
  if [[ "${current_uid}" != "0" && "$(${ID_BIN} -un)" == "${desktop_user}" ]]; then
    XDG_RUNTIME_DIR="${runtime_dir}" "${SYSTEMCTL_BIN}" --user "$@"
    return 0
  fi
  "${RUNUSER_BIN}" -u "${desktop_user}" -- env XDG_RUNTIME_DIR="${runtime_dir}" "${SYSTEMCTL_BIN}" --user "$@"
}

desktop_gui_uid_macos() {
  if [[ -n "${EXECUTOR_GUI_UID:-}" ]]; then
    printf '%s\n' "${EXECUTOR_GUI_UID}"
    return 0
  fi
  if [[ -n "${SUDO_UID:-}" ]]; then
    printf '%s\n' "${SUDO_UID}"
    return 0
  fi
  if gui_uid="$("${STAT_BIN}" -f %u /dev/console 2>/dev/null)"; then
    if [[ -n "${gui_uid}" && "${gui_uid}" != "0" ]]; then
      printf '%s\n' "${gui_uid}"
      return 0
    fi
  fi
  current_uid="$(${ID_BIN} -u)"
  if [[ "${current_uid}" != "0" ]]; then
    printf '%s\n' "${current_uid}"
    return 0
  fi
  printf 'unable to determine GUI owner UID\n' >&2
  exit 1
}

cloudflare_metadata_complete() {
  python3 - "${CONFIG_PATH}" "${CLOUDFLARED_TOKEN_PATH}" <<'PY'
import json, sys
cfg_path, expected_token_path = sys.argv[1], sys.argv[2]
with open(cfg_path, "r", encoding="utf-8") as fh:
    cfg = json.load(fh)
meta = cfg.get("cloudflare") or {}
required = ["account_id", "zone_id", "tunnel_id", "tunnel_name", "dns_record_id", "token_file_path", "hostname"]
missing = [name for name in required if not meta.get(name)]
if missing:
    raise SystemExit(1)
if meta["token_file_path"] != expected_token_path:
    print(f"Cloudflare token path mismatch: {meta['token_file_path']} != {expected_token_path}", file=sys.stderr)
    raise SystemExit(2)
PY
}

bootstrap_linux() {
  root="$1"
  install_managed_file "${TMP_BUNDLE}/systemd/executor-agent.service" "${root}/systemd/executor-agent.service" "systemd:executor-agent.service"
  install_managed_file "${TMP_BUNDLE}/systemd/executor-broker.service" "${root}/systemd/executor-broker.service" "systemd:executor-broker.service"
  install_managed_file "${TMP_BUNDLE}/systemd/cloudflared.service" "${root}/systemd/cloudflared.service" "systemd:cloudflared.service"
  install_managed_file "${TMP_BUNDLE}/systemd-user/executor-desktop.service" "${root}/systemd-user/executor-desktop.service" "systemd-user:executor-desktop.service"

  "${SYSTEMCTL_BIN}" daemon-reload
  "${SYSTEMCTL_BIN}" enable executor-agent.service executor-broker.service cloudflared.service
  "${SYSTEMCTL_BIN}" restart executor-agent.service executor-broker.service cloudflared.service
  run_desktop_systemctl enable executor-desktop.service
  run_desktop_systemctl restart executor-desktop.service
}

bootstrap_macos() {
  root="$1"
  install_managed_file "${TMP_BUNDLE}/LaunchDaemons/com.executor.agent.plist" "${root}/LaunchDaemons/com.executor.agent.plist" "launchd-system:com.executor.agent"
  install_managed_file "${TMP_BUNDLE}/LaunchDaemons/com.executor.broker.plist" "${root}/LaunchDaemons/com.executor.broker.plist" "launchd-system:com.executor.broker"
  install_managed_file "${TMP_BUNDLE}/LaunchDaemons/com.cloudflare.cloudflared.plist" "${root}/LaunchDaemons/com.cloudflare.cloudflared.plist" "launchd-system:com.cloudflare.cloudflared"
  install_managed_file "${TMP_BUNDLE}/LaunchAgents/com.executor.desktop.plist" "${root}/LaunchAgents/com.executor.desktop.plist" "launchd-gui:com.executor.desktop"

  gui_uid="$(desktop_gui_uid_macos)"
  for label in com.executor.agent com.executor.broker com.cloudflare.cloudflared; do
    "${LAUNCHCTL_BIN}" bootstrap system "${root}/LaunchDaemons/${label}.plist"
    "${LAUNCHCTL_BIN}" enable "system/${label}"
    "${LAUNCHCTL_BIN}" kickstart -k "system/${label}"
  done
  "${LAUNCHCTL_BIN}" bootstrap "gui/${gui_uid}" "${root}/LaunchAgents/com.executor.desktop.plist"
  "${LAUNCHCTL_BIN}" enable "gui/${gui_uid}/com.executor.desktop"
  "${LAUNCHCTL_BIN}" kickstart -k "gui/${gui_uid}/com.executor.desktop"
}

mkdir -p "${STATE_DIR}" "${DATA_DIR}" "$(dirname "${CONFIG_PATH}")" "$(dirname "${CLOUDFLARED_TOKEN_PATH}")" "${BACKUP_ROOT}"
: > "${MANIFEST_PATH}"

case "${TARGET}" in
  linux|wsl) ensure_linux_identity ;;
  macos) ensure_macos_identity ;;
esac

setup_cmd=("${EXECUTOR_BIN}" setup --domain "${DOMAIN}")
if [[ -n "${CLOUDFLARE_API_TOKEN_FILE}" ]]; then
  setup_cmd+=(--cloudflare-token-file "${CLOUDFLARE_API_TOKEN_FILE}")
fi
if [[ -n "${CLOUDFLARE_ACCOUNT_ID}" ]]; then
  setup_cmd+=(--cloudflare-account-id "${CLOUDFLARE_ACCOUNT_ID}")
fi
if [[ -n "${CLOUDFLARE_ZONE_ID}" ]]; then
  setup_cmd+=(--cloudflare-zone-id "${CLOUDFLARE_ZONE_ID}")
fi
if [[ -n "${CLOUDFLARE_TUNNEL_NAME}" ]]; then
  setup_cmd+=(--cloudflare-tunnel-name "${CLOUDFLARE_TUNNEL_NAME}")
fi
"${setup_cmd[@]}"

if ! cloudflare_metadata_complete; then
  printf 'Cloudflare setup incomplete. Provide CLOUDFLARE_API_TOKEN_FILE or pre-existing completed Cloudflare metadata before installing services.\n' >&2
  exit 1
fi

TMP_BUNDLE="$("${MKTEMP_BIN}" -d)"
"${EXECUTOR_BIN}" render-service-bundle \
  --target "${TARGET}" \
  --output "${TMP_BUNDLE}" \
  --binary-path "${EXECUTOR_BIN}" \
  --config-path "${CONFIG_PATH}" \
  --data-dir "${DATA_DIR}" \
  --log-path "${LOG_PATH}" \
  --cloudflared-binary-path "${CLOUDFLARED_BIN}" \
  --cloudflared-token-path "${CLOUDFLARED_TOKEN_PATH}" \
  --cloudflared-log-path "${CLOUDFLARED_LOG_PATH}" \
  --agent-user "${AGENT_USER}" \
  --agent-group "${AGENT_GROUP}" \
  --broker-user "${BROKER_USER}" \
  --broker-group "${BROKER_GROUP}" \
  --windows-agent-service "${WINDOWS_AGENT_SERVICE}"

ROOT="$(service_root)"
case "${TARGET}" in
  linux|wsl) bootstrap_linux "${ROOT}" ;;
  macos) bootstrap_macos "${ROOT}" ;;
esac

printf 'service bundle installed to %s\n' "${ROOT}"
printf 'manifest path: %s\n' "${MANIFEST_PATH}"
