#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUNDLE_ROOT="${EXECUTOR_BUNDLE_ROOT:-$(cd "${SCRIPT_DIR}/.." && pwd)}"
STATE_DIR="${EXECUTOR_STATE_DIR:-/var/lib/executor}"
TARGET="${EXECUTOR_TARGET:-$(uname | tr '[:upper:]' '[:lower:]')}"
DOMAIN="${EXECUTOR_DOMAIN:?set EXECUTOR_DOMAIN}"
EXECUTOR_INSTALL_BINARY_PATH="${EXECUTOR_INSTALL_BINARY_PATH:-/usr/local/bin/executor}"
EXECUTOR_KILL_INSTALL_BINARY_PATH="${EXECUTOR_KILL_INSTALL_BINARY_PATH:-/usr/local/bin/executor-kill}"
BUNDLED_EXECUTOR_PATH="${EXECUTOR_BUNDLED_BINARY_PATH:-${BUNDLE_ROOT}/executor}"
BUNDLED_EXECUTOR_KILL_PATH="${EXECUTOR_BUNDLED_KILL_BINARY_PATH:-${BUNDLE_ROOT}/executor-kill}"
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
CHOWN_BIN="${CHOWN_BIN:-chown}"
CHMOD_BIN="${CHMOD_BIN:-chmod}"
AGENT_USER=""
AGENT_GROUP=""
BROKER_USER="${EXECUTOR_BROKER_USER:-root}"
BROKER_GROUP="${EXECUTOR_BROKER_GROUP:-root}"
WINDOWS_AGENT_SERVICE="${EXECUTOR_WINDOWS_AGENT_SERVICE:-NT SERVICE\ExecutorAgent}"
MANIFEST_PATH="${STATE_DIR}/service-manifest.txt"
LEGACY_MANIFEST_PATH="${STATE_DIR}/legacy-cloudflared-manifest.txt"
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

if [[ "${TARGET}" == "macos" && -z "${EXECUTOR_BROKER_GROUP:-}" ]]; then
  BROKER_GROUP="wheel"
fi

service_root() {
  case "${TARGET}" in
    macos) printf '%s\n' "${EXECUTOR_INSTALL_ROOT:-/Library}" ;;
    linux|wsl) printf '%s\n' "${EXECUTOR_INSTALL_ROOT:-/etc}" ;;
    *) printf 'unsupported EXECUTOR_TARGET: %s\n' "${TARGET}" >&2; exit 1 ;;
  esac
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

replace_file_atomically() {
  src="$1"
  dst="$2"
  replacement="${dst}.executor-new.$$"
  rm -f "${replacement}"
  if ! cp "${src}" "${replacement}"; then
    rm -f "${replacement}"
    return 1
  fi
  if ! mv -f "${replacement}" "${dst}"; then
    rm -f "${replacement}"
    return 1
  fi
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
  replace_file_atomically "${src}" "${dst}"
  record_manifest "${label}" "${dst}" "${mode}"
}

install_managed_executable() {
  src="$1"
  dst="$2"
  label="$3"
  if [[ ! -f "${src}" ]]; then
    printf 'missing bundled binary: %s\n' "${src}" >&2
    exit 1
  fi
  install_managed_file "${src}" "${dst}" "${label}"
  chmod 0755 "${dst}"
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

desktop_user_macos() {
  if [[ -n "${EXECUTOR_DESKTOP_USER:-}" ]]; then
    printf '%s\n' "${EXECUTOR_DESKTOP_USER}"
    return 0
  fi
  if [[ -n "${SUDO_USER:-}" ]]; then
    printf '%s\n' "${SUDO_USER}"
    return 0
  fi
  if console_user="$("${STAT_BIN}" -f %Su /dev/console 2>/dev/null)"; then
    if [[ -n "${console_user}" && "${console_user}" != "root" ]]; then
      printf '%s\n' "${console_user}"
      return 0
    fi
  fi
  current_uid="$(${ID_BIN} -u)"
  if [[ "${current_uid}" != "0" ]]; then
    "${ID_BIN}" -un
    return 0
  fi
  printf 'unable to determine desktop owner user\n' >&2
  exit 1
}

desktop_group_for_user() {
  "${ID_BIN}" -gn "$1"
}

set_owner_readable_tree() {
  path="$1"
  if [[ -e "${path}" ]]; then
    "${CHOWN_BIN}" -R "${AGENT_USER}:${AGENT_GROUP}" "${path}"
    "${CHMOD_BIN}" -R u+rwX,go-rwx "${path}"
  fi
}

set_owner_readable_file() {
  path="$1"
  if [[ -f "${path}" ]]; then
    "${CHOWN_BIN}" "${AGENT_USER}:${AGENT_GROUP}" "${path}"
    "${CHMOD_BIN}" u+rw,go-rwx "${path}"
  fi
}

configure_unix_state_permissions() {
  set_owner_readable_tree "${STATE_DIR}"
  if [[ "${DATA_DIR}" != "${STATE_DIR}" ]]; then
    set_owner_readable_tree "${DATA_DIR}"
  fi
  config_parent="$(dirname "${CONFIG_PATH}")"
  if [[ "${config_parent}" != "${STATE_DIR}" ]]; then
    set_owner_readable_tree "${config_parent}"
  fi
  token_parent="$(dirname "${CLOUDFLARED_TOKEN_PATH}")"
  if [[ "${token_parent}" != "${STATE_DIR}" && "${token_parent}" != "${config_parent}" ]]; then
    set_owner_readable_tree "${token_parent}"
  fi
  set_owner_readable_file "${CONFIG_PATH}"
  set_owner_readable_file "${STATE_DIR}/secrets.json"
  set_owner_readable_file "${CLOUDFLARED_TOKEN_PATH}"
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

discover_owned_legacy_cloudflared() {
	case "${TARGET}" in
		linux|wsl)
			LEGACY_CLOUDFLARED_PATH="$(service_root)/systemd/system/cloudflared.service"
			LEGACY_CLOUDFLARED_LABEL="systemd:cloudflared.service"
			;;
		macos)
			LEGACY_CLOUDFLARED_PATH="$(service_root)/LaunchDaemons/com.cloudflare.cloudflared.plist"
			LEGACY_CLOUDFLARED_LABEL="launchd-system:com.cloudflare.cloudflared"
			;;
		*) return 0 ;;
	esac
	legacy_source=""
	if [[ -f "${LEGACY_MANIFEST_PATH}" ]]; then
		legacy_source="${LEGACY_MANIFEST_PATH}"
	elif [[ -f "${MANIFEST_PATH}" ]]; then
		legacy_source="${MANIFEST_PATH}"
	fi
	[[ -n "${legacy_source}" ]] || return 0
	LEGACY_CLOUDFLARED_MODE=""
	while IFS='|' read -r label path mode; do
		if [[ "${label}" == "${LEGACY_CLOUDFLARED_LABEL}" && "${path}" == "${LEGACY_CLOUDFLARED_PATH}" ]]; then
			LEGACY_CLOUDFLARED_MODE="${mode}"
			break
		fi
	done < "${legacy_source}"
	[[ -n "${LEGACY_CLOUDFLARED_MODE}" ]] || return 0
	if [[ "${legacy_source}" == "${MANIFEST_PATH}" ]]; then
		cp "${MANIFEST_PATH}" "${LEGACY_MANIFEST_PATH}"
	fi
}

migrate_owned_legacy_cloudflared() {
	[[ -n "${LEGACY_CLOUDFLARED_MODE:-}" ]] || return 0
	if [[ "${LEGACY_CLOUDFLARED_MODE}" != "restore" && "${LEGACY_CLOUDFLARED_MODE}" != "remove" ]]; then
		printf 'unsupported legacy cloudflared manifest mode: %s\n' "${LEGACY_CLOUDFLARED_MODE}" >&2
		return 1
	fi
	legacy_backup="${BACKUP_ROOT}${LEGACY_CLOUDFLARED_PATH}"
	if [[ "${LEGACY_CLOUDFLARED_MODE}" == "restore" && ! -f "${legacy_backup}" ]]; then
		printf 'legacy cloudflared backup is missing: %s\n' "${legacy_backup}" >&2
		return 1
	fi
	case "${TARGET}" in
		linux|wsl)
			legacy_was_active=0
			legacy_was_enabled=0
			"${SYSTEMCTL_BIN}" is-active --quiet cloudflared.service && legacy_was_active=1 || true
			"${SYSTEMCTL_BIN}" is-enabled --quiet cloudflared.service && legacy_was_enabled=1 || true
			"${SYSTEMCTL_BIN}" stop cloudflared.service
			"${SYSTEMCTL_BIN}" disable cloudflared.service
			;;
		macos)
			legacy_was_loaded=0
			"${LAUNCHCTL_BIN}" print system/com.cloudflare.cloudflared >/dev/null 2>&1 && legacy_was_loaded=1 || true
			"${LAUNCHCTL_BIN}" bootout system/com.cloudflare.cloudflared 2>/dev/null || true
			;;
	esac
	if [[ "${LEGACY_CLOUDFLARED_MODE}" == "restore" && -f "${legacy_backup}" ]]; then
		mkdir -p "$(dirname "${LEGACY_CLOUDFLARED_PATH}")"
		cp "${legacy_backup}" "${LEGACY_CLOUDFLARED_PATH}"
	else
		rm -f "${LEGACY_CLOUDFLARED_PATH}"
	fi
	if [[ "${TARGET}" == "linux" || "${TARGET}" == "wsl" ]]; then
		"${SYSTEMCTL_BIN}" daemon-reload
		if [[ "${LEGACY_CLOUDFLARED_MODE}" == "restore" ]]; then
			if [[ "${legacy_was_enabled}" == "1" ]]; then
				"${SYSTEMCTL_BIN}" enable cloudflared.service
			fi
			if [[ "${legacy_was_active}" == "1" ]]; then
				"${SYSTEMCTL_BIN}" start cloudflared.service
			fi
		fi
	elif [[ "${TARGET}" == "macos" && "${LEGACY_CLOUDFLARED_MODE}" == "restore" && "${legacy_was_loaded}" == "1" ]]; then
		"${LAUNCHCTL_BIN}" bootstrap system "${LEGACY_CLOUDFLARED_PATH}"
		"${LAUNCHCTL_BIN}" enable system/com.cloudflare.cloudflared
		"${LAUNCHCTL_BIN}" kickstart -k system/com.cloudflare.cloudflared
	fi
	rm -f "${LEGACY_MANIFEST_PATH}"
}

bootstrap_linux() {
  root="$1"
  install_managed_file "${TMP_BUNDLE}/systemd/executor-agent.service" "${root}/systemd/system/executor-agent.service" "systemd:executor-agent.service"
  install_managed_file "${TMP_BUNDLE}/systemd/executor-broker.service" "${root}/systemd/system/executor-broker.service" "systemd:executor-broker.service"
  install_managed_file "${TMP_BUNDLE}/systemd/executor-dashboard.service" "${root}/systemd/system/executor-dashboard.service" "systemd:executor-dashboard.service"
  install_managed_file "${TMP_BUNDLE}/systemd/executor-cloudflared.service" "${root}/systemd/system/executor-cloudflared.service" "systemd:executor-cloudflared.service"
  install_managed_file "${TMP_BUNDLE}/systemd-user/executor-desktop.service" "${root}/systemd/user/executor-desktop.service" "systemd-user:executor-desktop.service"

  "${SYSTEMCTL_BIN}" daemon-reload
  "${SYSTEMCTL_BIN}" enable executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service
  "${SYSTEMCTL_BIN}" restart executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service
  run_desktop_systemctl enable executor-desktop.service
  run_desktop_systemctl restart executor-desktop.service
}

wait_launchd_unloaded() {
  target="$1"
  for _ in {1..50}; do
    if ! "${LAUNCHCTL_BIN}" print "${target}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  printf 'timed out waiting for launchd service to unload: %s\n' "${target}" >&2
  return 1
}

bootstrap_macos() {
  root="$1"
  install_managed_file "${TMP_BUNDLE}/LaunchDaemons/com.executor.agent.plist" "${root}/LaunchDaemons/com.executor.agent.plist" "launchd-system:com.executor.agent"
  install_managed_file "${TMP_BUNDLE}/LaunchDaemons/com.executor.broker.plist" "${root}/LaunchDaemons/com.executor.broker.plist" "launchd-system:com.executor.broker"
  install_managed_file "${TMP_BUNDLE}/LaunchDaemons/com.executor.dashboard.plist" "${root}/LaunchDaemons/com.executor.dashboard.plist" "launchd-system:com.executor.dashboard"
  install_managed_file "${TMP_BUNDLE}/LaunchDaemons/com.executor.cloudflared.plist" "${root}/LaunchDaemons/com.executor.cloudflared.plist" "launchd-system:com.executor.cloudflared"
  install_managed_file "${TMP_BUNDLE}/LaunchAgents/com.executor.desktop.plist" "${root}/LaunchAgents/com.executor.desktop.plist" "launchd-gui:com.executor.desktop"

  gui_uid="$(desktop_gui_uid_macos)"
  for label in com.executor.agent com.executor.broker com.executor.dashboard com.executor.cloudflared; do
    "${LAUNCHCTL_BIN}" bootout "system/${label}" 2>/dev/null || true
    wait_launchd_unloaded "system/${label}"
    "${LAUNCHCTL_BIN}" bootstrap system "${root}/LaunchDaemons/${label}.plist"
    "${LAUNCHCTL_BIN}" enable "system/${label}"
    "${LAUNCHCTL_BIN}" kickstart -k "system/${label}"
  done
  "${LAUNCHCTL_BIN}" bootout "gui/${gui_uid}/com.executor.desktop" 2>/dev/null || true
  wait_launchd_unloaded "gui/${gui_uid}/com.executor.desktop"
  "${LAUNCHCTL_BIN}" bootstrap "gui/${gui_uid}" "${root}/LaunchAgents/com.executor.desktop.plist"
  "${LAUNCHCTL_BIN}" enable "gui/${gui_uid}/com.executor.desktop"
  "${LAUNCHCTL_BIN}" kickstart -k "gui/${gui_uid}/com.executor.desktop"
}

mkdir -p "${STATE_DIR}" "${DATA_DIR}" "$(dirname "${CONFIG_PATH}")" "$(dirname "${CLOUDFLARED_TOKEN_PATH}")" "${BACKUP_ROOT}"
discover_owned_legacy_cloudflared
: > "${MANIFEST_PATH}"

case "${TARGET}" in
  linux|wsl)
    AGENT_USER="$(desktop_user_linux)"
    AGENT_GROUP="$(desktop_group_for_user "${AGENT_USER}")"
    ;;
  macos)
    AGENT_USER="$(desktop_user_macos)"
    AGENT_GROUP="$(desktop_group_for_user "${AGENT_USER}")"
    ;;
esac

install_managed_executable "${BUNDLED_EXECUTOR_PATH}" "${EXECUTOR_INSTALL_BINARY_PATH}" "file"
install_managed_executable "${BUNDLED_EXECUTOR_KILL_PATH}" "${EXECUTOR_KILL_INSTALL_BINARY_PATH}" "file"

setup_cmd=("${EXECUTOR_INSTALL_BINARY_PATH}" setup --domain "${DOMAIN}")
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

case "${TARGET}" in
  linux|wsl|macos) configure_unix_state_permissions ;;
esac

TMP_BUNDLE="$("${MKTEMP_BIN}" -d)"
"${EXECUTOR_INSTALL_BINARY_PATH}" render-service-bundle \
  --target "${TARGET}" \
  --output "${TMP_BUNDLE}" \
  --binary-path "${EXECUTOR_INSTALL_BINARY_PATH}" \
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
migrate_owned_legacy_cloudflared

printf 'service bundle installed to %s\n' "${ROOT}"
printf 'manifest path: %s\n' "${MANIFEST_PATH}"
