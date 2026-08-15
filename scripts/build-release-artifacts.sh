#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${OUT_DIR:-${ROOT_DIR}/dist/release}"
BUILD_TARGETS="${EXECUTOR_BUILD_TARGETS:-darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64}"

mkdir -p "${OUT_DIR}"
rm -f "${OUT_DIR}"/SHA256SUMS.txt

for target in ${BUILD_TARGETS}; do
  IFS=/ read -r GOOS GOARCH <<<"${target}"
  ARCHIVE_NAME="executor_${GOOS}_${GOARCH}"
  STAGING_DIR="$(mktemp -d)"
  EXECUTOR_BINARY="executor"
  KILL_BINARY="executor-kill"
  if [[ "${GOOS}" == "windows" ]]; then
    EXECUTOR_BINARY="${EXECUTOR_BINARY}.exe"
    KILL_BINARY="${KILL_BINARY}.exe"
  fi

  GOOS="${GOOS}" GOARCH="${GOARCH}" go build -o "${STAGING_DIR}/${EXECUTOR_BINARY}" ./packaging/cmd/executor
  GOOS="${GOOS}" GOARCH="${GOARCH}" go build -o "${STAGING_DIR}/${KILL_BINARY}" ./cmd/executor-kill
  cp -R "${ROOT_DIR}/scripts" "${STAGING_DIR}/scripts"
  mkdir -p "${STAGING_DIR}/docs"
  cp "${ROOT_DIR}/docs/DEPLOYMENT.md" "${STAGING_DIR}/docs/DEPLOYMENT.md"

  if [[ "${GOOS}" == "windows" ]]; then
    (cd "${STAGING_DIR}" && zip -qr "${OUT_DIR}/${ARCHIVE_NAME}.zip" .)
    sha256sum "${OUT_DIR}/${ARCHIVE_NAME}.zip" >> "${OUT_DIR}/SHA256SUMS.txt"
  else
    tar -C "${STAGING_DIR}" -czf "${OUT_DIR}/${ARCHIVE_NAME}.tar.gz" .
    sha256sum "${OUT_DIR}/${ARCHIVE_NAME}.tar.gz" >> "${OUT_DIR}/SHA256SUMS.txt"
  fi
done
