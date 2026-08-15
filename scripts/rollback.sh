#!/usr/bin/env bash
set -euo pipefail

STATE_DIR="${EXECUTOR_STATE_DIR:-/var/lib/executor}"
TARGET="${EXECUTOR_TARGET:-$(uname | tr '[:upper:]' '[:lower:]')}"
SYSTEMCTL_BIN="${SYSTEMCTL_BIN:-systemctl}"
LAUNCHCTL_BIN="${LAUNCHCTL_BIN:-launchctl}"
ID_BIN="${ID_BIN:-id}"
STAT_BIN="${STAT_BIN:-stat}"
RUNUSER_BIN="${RUNUSER_BIN:-runuser}"
MANIFEST_PATH="${STATE_DIR}/service-manifest.txt"
BACKUP_ROOT="${STATE_DIR}/service-backups"

case "${TARGET}" in
  darwin) TARGET="macos" ;;
  linux) TARGET="linux" ;;
esac

if [[ ! -f "${MANIFEST_PATH}" ]]; then
  printf 'no manifest found at %s\n' "${MANIFEST_PATH}"
  exit 0
fi

stop_linux() {
  "${SYSTEMCTL_BIN}" stop executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service
  "${SYSTEMCTL_BIN}" disable executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service
  desktop_user="$(desktop_user_linux)" || return 0
  desktop_uid="$(${ID_BIN} -u "${desktop_user}")"
  runtime_dir="/run/user/${desktop_uid}"
  current_uid="$(${ID_BIN} -u)"
  if [[ "${current_uid}" != "0" && "$(${ID_BIN} -un)" == "${desktop_user}" ]]; then
    XDG_RUNTIME_DIR="${runtime_dir}" "${SYSTEMCTL_BIN}" --user stop executor-desktop.service
    XDG_RUNTIME_DIR="${runtime_dir}" "${SYSTEMCTL_BIN}" --user disable executor-desktop.service
    return 0
  fi
  "${RUNUSER_BIN}" -u "${desktop_user}" -- env XDG_RUNTIME_DIR="${runtime_dir}" "${SYSTEMCTL_BIN}" --user stop executor-desktop.service
  "${RUNUSER_BIN}" -u "${desktop_user}" -- env XDG_RUNTIME_DIR="${runtime_dir}" "${SYSTEMCTL_BIN}" --user disable executor-desktop.service
}

stop_macos() {
  gui_uid="$(desktop_gui_uid_macos)"
  for label in com.executor.agent com.executor.broker com.executor.dashboard com.executor.cloudflared; do
    "${LAUNCHCTL_BIN}" bootout "system/${label}" || true
    "${LAUNCHCTL_BIN}" disable "system/${label}" || true
  done
  "${LAUNCHCTL_BIN}" bootout "gui/${gui_uid}/com.executor.desktop" || true
  "${LAUNCHCTL_BIN}" disable "gui/${gui_uid}/com.executor.desktop" || true
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

case "${TARGET}" in
  linux|wsl) stop_linux ;;
  macos) stop_macos ;;
esac

while IFS='|' read -r label path mode; do
  if [[ "${label}" == "identity:user" ]]; then
    continue
  fi
  backup="${BACKUP_ROOT}${path}"
  if [[ "${mode}" == "restore" && -f "${backup}" ]]; then
    mkdir -p "$(dirname "${path}")"
    cp "${backup}" "${path}"
  else
    rm -f "${path}"
  fi
done < "${MANIFEST_PATH}"

case "${TARGET}" in
  linux|wsl) "${SYSTEMCTL_BIN}" daemon-reload ;;
esac

printf 'restored managed services from %s\n' "${MANIFEST_PATH}"
