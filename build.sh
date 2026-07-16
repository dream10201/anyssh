#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="${ROOT_DIR}/bin"
CLIENT_ASSET_DIR="${ROOT_DIR}/internal/server/assets"
TARGET_GOOS="${GOOS:-$(go env GOOS)}"
TARGET_GOARCH="${GOARCH:-$(go env GOARCH)}"

if [[ "${TARGET_GOOS}" != "linux" ]]; then
  echo "The one-line installer currently supports Linux targets only (got ${TARGET_GOOS})." >&2
  exit 1
fi

mkdir -p "${BIN_DIR}"

echo "Building Linux clients (amd64, arm64, armv5+)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o "${CLIENT_ASSET_DIR}/anyssh-client-linux-amd64" "${ROOT_DIR}/cmd/anyssh-client"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o "${CLIENT_ASSET_DIR}/anyssh-client-linux-arm64" "${ROOT_DIR}/cmd/anyssh-client"
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=5 \
  go build -trimpath -o "${CLIENT_ASSET_DIR}/anyssh-client-linux-arm" "${ROOT_DIR}/cmd/anyssh-client"
rm -f "${CLIENT_ASSET_DIR}/anyssh-client"
rm -f "${BIN_DIR}/anyssh-client"

echo "Building anyssh-server with embedded multi-architecture clients..."
GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" \
  CGO_ENABLED=0 go build -trimpath -o "${BIN_DIR}/anyssh-server" "${ROOT_DIR}/cmd/anyssh-server"

echo "Build complete: ${BIN_DIR}/anyssh-server"
