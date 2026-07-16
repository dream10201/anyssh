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

CLIENT_ARCHES=(386 amd64 arm arm64 loong64 mips mips64 mips64le mipsle ppc64 ppc64le riscv64 s390x)

echo "Building Linux clients (${CLIENT_ARCHES[*]})..."
build_client() {
  local arch="$1"
  case "${arch}" in
    386)
      CGO_ENABLED=0 GOOS=linux GOARCH=386 GO386=softfloat \
        go build -trimpath -o "${CLIENT_ASSET_DIR}/anyssh-client-linux-${arch}" "${ROOT_DIR}/cmd/anyssh-client"
      ;;
    arm)
      CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=5 \
        go build -trimpath -o "${CLIENT_ASSET_DIR}/anyssh-client-linux-${arch}" "${ROOT_DIR}/cmd/anyssh-client"
      ;;
    mips|mipsle)
      CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" GOMIPS=softfloat \
        go build -trimpath -o "${CLIENT_ASSET_DIR}/anyssh-client-linux-${arch}" "${ROOT_DIR}/cmd/anyssh-client"
      ;;
    mips64|mips64le)
      CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" GOMIPS64=softfloat \
        go build -trimpath -o "${CLIENT_ASSET_DIR}/anyssh-client-linux-${arch}" "${ROOT_DIR}/cmd/anyssh-client"
      ;;
    *)
      CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" \
        go build -trimpath -o "${CLIENT_ASSET_DIR}/anyssh-client-linux-${arch}" "${ROOT_DIR}/cmd/anyssh-client"
      ;;
  esac
}

pids=()
for arch in "${CLIENT_ARCHES[@]}"; do
  build_client "${arch}" &
  pids+=("$!")
  if (( ${#pids[@]} == 4 )); then
    for pid in "${pids[@]}"; do wait "${pid}"; done
    pids=()
  fi
done
for pid in "${pids[@]}"; do wait "${pid}"; done
rm -f "${CLIENT_ASSET_DIR}/anyssh-client"
rm -f "${BIN_DIR}/anyssh-client"

echo "Building anyssh-server with embedded multi-architecture clients..."
GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" \
  CGO_ENABLED=0 go build -trimpath -o "${BIN_DIR}/anyssh-server" "${ROOT_DIR}/cmd/anyssh-server"

echo "Build complete: ${BIN_DIR}/anyssh-server"
