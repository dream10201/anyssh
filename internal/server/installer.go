package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"anyssh/internal/bootstrap"
)

var clientArchitectures = []string{
	"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64", "mips64le",
	"mipsle", "ppc64", "ppc64le", "riscv64", "s390x",
}

func (s *Server) handleClientDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	arch := strings.TrimPrefix(r.URL.Path, "/download/anyssh-client/")
	if r.URL.Path == "/download/anyssh-client" || !validClientArchitecture(arch) {
		http.Error(w, "unsupported client architecture", http.StatusBadRequest)
		return
	}
	binary, err := s.configuredClient(r, arch)
	if err != nil {
		http.Error(w, "embedded client is unavailable; rebuild the server with build.sh", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="anyssh-client"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "anyssh-client", time.Time{}, bytes.NewReader(binary))
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	serverURL := s.installServerURL(r)
	checksums := make(map[string]string, len(clientArchitectures))
	for _, arch := range clientArchitectures {
		binary, err := s.configuredClient(r, arch)
		if err != nil {
			http.Error(w, "embedded client is unavailable; rebuild the server with build.sh", http.StatusServiceUnavailable)
			return
		}
		sum := sha256.Sum256(binary)
		checksums[arch] = hex.EncodeToString(sum[:])
	}
	script := renderInstallScript(serverURL, checksums)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(script))
}

func (s *Server) configuredClient(r *http.Request, arch string) ([]byte, error) {
	if !validClientArchitecture(arch) {
		return nil, fmt.Errorf("unsupported client architecture %q", arch)
	}
	binary, err := webFiles.ReadFile("assets/anyssh-client-linux-" + arch)
	if err != nil || len(binary) == 0 {
		return nil, fmt.Errorf("read embedded client: %w", err)
	}
	return bootstrap.Append(binary, bootstrap.Config{
		ServerURL:  s.installServerURL(r),
		Secret:     s.secret,
		Rotate:     s.clientRotate.String(),
		NotifyURL:  s.notifyURL,
		NotifyUser: s.notifyUser,
	})
}

func validClientArchitecture(arch string) bool {
	for _, supported := range clientArchitectures {
		if arch == supported {
			return true
		}
	}
	return false
}

func (s *Server) installServerURL(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := r.Host
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}

func renderInstallScript(serverURL string, checksums map[string]string) string {
	var checksumCases strings.Builder
	for _, arch := range clientArchitectures {
		fmt.Fprintf(&checksumCases, "  %s) EXPECTED_SHA256=%s ;;\n", arch, shellQuote(checksums[arch]))
	}
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

SERVER_URL=%s

fail() { echo "anyssh install: $*" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"

normalize_arch() {
  local raw="${1,,}"
  raw="${raw#"${raw%%%%[![:space:]]*}"}"
  raw="${raw%%"${raw##*[![:space:]]}"}"
  case "$raw" in
    i386|i486|i586|i686|x86) echo 386 ;;
    x86_64|x86-64|amd64) echo amd64 ;;
    arm|armel|armhf|armv5*|armv6*|armv7*|armv8l) echo arm ;;
    aarch64|arm64) echo arm64 ;;
    loongarch64|loong64) echo loong64 ;;
    mips64el|mips64le) echo mips64le ;;
    mips64) echo mips64 ;;
    mipsel|mipsle) echo mipsle ;;
    mips) echo mips ;;
    ppc64el|ppc64le) echo ppc64le ;;
    ppc64) echo ppc64 ;;
    riscv64|rv64*) echo riscv64 ;;
    s390x) echo s390x ;;
    *) return 1 ;;
  esac
}

elf_arch() {
  command -v od >/dev/null 2>&1 || return 1
  local elf=/bin/sh class data b1 b2 machine
  [[ -r "$elf" ]] || elf=/proc/self/exe
  [[ -r "$elf" ]] || return 1
  read -r class data < <(od -An -tu1 -j4 -N2 "$elf") || return 1
  read -r b1 b2 < <(od -An -tu1 -j18 -N2 "$elf") || return 1
  [[ -n "$class" && -n "$data" && -n "$b1" && -n "$b2" ]] || return 1
  if [[ "$data" == 1 ]]; then machine=$((b1 + b2 * 256)); else machine=$((b1 * 256 + b2)); fi
  case "$machine:$class:$data" in
    3:1:1) echo 386 ;;
    8:1:1) echo mipsle ;;
    8:1:2) echo mips ;;
    8:2:1) echo mips64le ;;
    8:2:2) echo mips64 ;;
    21:2:1) echo ppc64le ;;
    21:2:2) echo ppc64 ;;
    22:2:*) echo s390x ;;
    40:1:1) echo arm ;;
    62:2:1) echo amd64 ;;
    183:2:1) echo arm64 ;;
    243:2:1) echo riscv64 ;;
    258:2:1) echo loong64 ;;
    *) return 1 ;;
  esac
}

detect_arch() {
  local raw key
  if command -v uname >/dev/null 2>&1; then
    raw="$(uname -m 2>/dev/null || true)"; normalize_arch "$raw" && return
  fi
  if command -v arch >/dev/null 2>&1; then
    raw="$(arch 2>/dev/null || true)"; normalize_arch "$raw" && return
  fi
  if command -v busybox >/dev/null 2>&1; then
    raw="$(busybox uname -m 2>/dev/null || true)"; normalize_arch "$raw" && return
  fi
  if command -v dpkg >/dev/null 2>&1; then
    raw="$(dpkg --print-architecture 2>/dev/null || true)"; normalize_arch "$raw" && return
  fi
  if command -v rpm >/dev/null 2>&1; then
    raw="$(rpm --eval '%%{_arch}' 2>/dev/null || true)"; normalize_arch "$raw" && return
  fi
  if command -v apk >/dev/null 2>&1; then
    raw="$(apk --print-arch 2>/dev/null || true)"; normalize_arch "$raw" && return
  fi
  if command -v getconf >/dev/null 2>&1; then
    for key in MACHINE_ARCH HOSTTYPE; do
      raw="$(getconf "$key" 2>/dev/null || true)"; normalize_arch "$raw" && return
    done
  fi
  if [[ -r /proc/cpuinfo ]]; then
    while IFS=: read -r key raw; do normalize_arch "$raw" && return; done < /proc/cpuinfo
  fi
  elf_arch
}

ACTUAL_ARCH="$(detect_arch)" || fail "cannot detect a supported Linux architecture"
case "$ACTUAL_ARCH" in
%s  *) fail "unsupported architecture: $ACTUAL_ARCH" ;;
esac

TMP_FILE="$(mktemp)"
trap 'rm -f "$TMP_FILE"' EXIT
curl -fsSL "$SERVER_URL/download/anyssh-client/$ACTUAL_ARCH" -o "$TMP_FILE"
echo "$EXPECTED_SHA256  $TMP_FILE" | sha256sum -c - >/dev/null
chmod 0755 "$TMP_FILE"

if [[ "$(id -u)" -eq 0 ]]; then
  TARGET_USER="${SUDO_USER:-root}"
  TARGET_HOME="$(getent passwd "$TARGET_USER" | cut -d: -f6)"
  [[ -n "$TARGET_HOME" ]] || fail "cannot determine home directory for $TARGET_USER"
  install -m 0755 "$TMP_FILE" /usr/local/bin/anyssh-client
  if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    cat > /etc/systemd/system/anyssh-client.service <<UNIT
[Unit]
Description=AnySSH reverse web terminal client
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$TARGET_USER
Environment=HOME=$TARGET_HOME
WorkingDirectory=$TARGET_HOME
ExecStart=/usr/local/bin/anyssh-client
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
    systemctl daemon-reload
    systemctl enable --now anyssh-client.service
    echo "AnySSH client installed and started with systemd."
  else
    if [[ -f /run/anyssh-client.pid ]]; then
      kill "$(cat /run/anyssh-client.pid)" 2>/dev/null || true
    fi
    nohup /usr/local/bin/anyssh-client >> /var/log/anyssh-client.log 2>&1 < /dev/null &
    echo $! > /run/anyssh-client.pid
    echo "AnySSH client installed and started in the background (PID $!)."
  fi
else
  INSTALL_DIR="$HOME/.local/share/anyssh"
  install -d -m 0700 "$INSTALL_DIR"
  install -m 0755 "$TMP_FILE" "$INSTALL_DIR/anyssh-client"
  if [[ -f "$INSTALL_DIR/client.pid" ]]; then
    OLD_PID="$(cat "$INSTALL_DIR/client.pid")"
    kill "$OLD_PID" 2>/dev/null || true
  fi
  nohup "$INSTALL_DIR/anyssh-client" >> "$INSTALL_DIR/client.log" 2>&1 < /dev/null &
  echo $! > "$INSTALL_DIR/client.pid"
  echo "AnySSH client installed and started in the background (PID $!)."
fi
`, shellQuote(serverURL), checksumCases.String())
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
