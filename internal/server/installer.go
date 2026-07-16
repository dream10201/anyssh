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
		ServerURL: s.installServerURL(r),
		Secret:    s.secret,
		Rotate:    s.clientRotate.String(),
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
	return fmt.Sprintf(`#!/bin/sh
set -eu
set -f

SERVER_URL=%s

fail() { echo "anyssh install: $*" >&2; exit 1; }
download() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget -qO "$2" "$1"
  elif command -v busybox >/dev/null 2>&1; then busybox wget -qO "$2" "$1"
  else fail "curl, wget, or BusyBox wget is required"; fi
}
verify_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then echo "$1  $2" | sha256sum -c - >/dev/null
  elif command -v busybox >/dev/null 2>&1; then echo "$1  $2" | busybox sha256sum -c - >/dev/null
  elif command -v openssl >/dev/null 2>&1; then actual=""; for word in $(openssl dgst -sha256 "$2"); do actual="$word"; done; [ "$actual" = "$1" ]
  else echo "anyssh install: WARNING: no SHA-256 tool found; installing without download verification" >&2; return 0; fi || fail "client checksum verification failed"
}
start_background() {
  executable="$1"; log_file="$2"; pid_file="$3"
  if command -v nohup >/dev/null 2>&1; then nohup "$executable" >> "$log_file" 2>&1 < /dev/null &
  elif command -v busybox >/dev/null 2>&1; then busybox nohup "$executable" >> "$log_file" 2>&1 < /dev/null &
  else fail "nohup or BusyBox nohup is required"; fi
  new_pid=$!
  echo "$new_pid" > "$pid_file"
  echo "AnySSH client installed or updated and restarted in the background (PID $new_pid)."
}

normalize_arch() {
  raw="$1"; set -- $raw; raw="${1:-}"
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
  elf=/bin/sh; [ -r "$elf" ] || elf=/proc/self/exe; [ -r "$elf" ] || return 1
  set -- $(od -An -tu1 -j4 -N2 "$elf") || return 1; class="${1:-}"; data="${2:-}"
  set -- $(od -An -tu1 -j18 -N2 "$elf") || return 1; b1="${1:-}"; b2="${2:-}"
  [ -n "$class" ] && [ -n "$data" ] && [ -n "$b1" ] && [ -n "$b2" ] || return 1
  if [ "$data" = 1 ]; then machine=$((b1 + b2 * 256)); else machine=$((b1 * 256 + b2)); fi
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
  if command -v uname >/dev/null 2>&1; then
    raw="$(uname -m 2>/dev/null || :)"; normalize_arch "$raw" && return
  fi
  if command -v arch >/dev/null 2>&1; then
    raw="$(arch 2>/dev/null || :)"; normalize_arch "$raw" && return
  fi
  if command -v busybox >/dev/null 2>&1; then
    raw="$(busybox uname -m 2>/dev/null || :)"; normalize_arch "$raw" && return
  fi
  if command -v dpkg >/dev/null 2>&1; then
    raw="$(dpkg --print-architecture 2>/dev/null || :)"; normalize_arch "$raw" && return
  fi
  if command -v rpm >/dev/null 2>&1; then
    raw="$(rpm --eval '%%{_arch}' 2>/dev/null || :)"; normalize_arch "$raw" && return
  fi
  if command -v apk >/dev/null 2>&1; then
    raw="$(apk --print-arch 2>/dev/null || :)"; normalize_arch "$raw" && return
  fi
  if command -v getconf >/dev/null 2>&1; then
    for key in MACHINE_ARCH HOSTTYPE; do
      raw="$(getconf "$key" 2>/dev/null || :)"; normalize_arch "$raw" && return
    done
  fi
  if [ -r /proc/cpuinfo ]; then
    while IFS=: read -r key raw; do normalize_arch "$raw" && return; done < /proc/cpuinfo
  fi
  elf_arch
}

ACTUAL_ARCH="$(detect_arch)" || fail "cannot detect a supported Linux architecture"
case "$ACTUAL_ARCH" in
%s  *) fail "unsupported architecture: $ACTUAL_ARCH" ;;
esac

if command -v mktemp >/dev/null 2>&1; then TMP_FILE="$(mktemp)"; else TMP_FILE="${TMPDIR:-/tmp}/anyssh-client.$$"; fi
trap 'rm -f "$TMP_FILE"' 0 HUP INT TERM
download "$SERVER_URL/download/anyssh-client/$ACTUAL_ARCH" "$TMP_FILE"
verify_sha256 "$EXPECTED_SHA256" "$TMP_FILE"
chmod 0755 "$TMP_FILE"

stop_pid_file() {
  pid_file="$1"; expected_exe="$2"
  [ -f "$pid_file" ] || return 0
  pid="$(cat "$pid_file" 2>/dev/null || :)"
  case "$pid" in ''|*[!0-9]*) rm -f "$pid_file"; return 0 ;; esac
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$pid_file"
    return 0
  fi
  if [ -e "/proc/$pid/exe" ] && command -v readlink >/dev/null 2>&1; then
    current_exe="$(readlink "/proc/$pid/exe" 2>/dev/null || :)"
    if [ "$current_exe" != "$expected_exe" ] && [ "$current_exe" != "$expected_exe (deleted)" ]; then
      echo "Skipping stale PID file $pid_file (PID $pid belongs to $current_exe)" >&2
      rm -f "$pid_file"
      return 0
    fi
  fi
  kill "$pid" 2>/dev/null || :
  attempts=0
  while [ "$attempts" -lt 5 ]; do
    kill -0 "$pid" 2>/dev/null || break
    sleep 1
    attempts=$((attempts + 1))
  done
  kill -9 "$pid" 2>/dev/null || :
  rm -f "$pid_file"
}

if [ "$(id -u)" -eq 0 ]; then
  TARGET_USER="${SUDO_USER:-root}"
  TARGET_HOME=""
  if command -v getent >/dev/null 2>&1 && command -v cut >/dev/null 2>&1; then TARGET_HOME="$(getent passwd "$TARGET_USER" | cut -d: -f6)"; fi
  if [ -z "$TARGET_HOME" ] && [ -r /etc/passwd ]; then
    while IFS=: read -r name _ _ _ _ home _; do [ "$name" = "$TARGET_USER" ] && TARGET_HOME="$home"; done < /etc/passwd
  fi
  [ -n "$TARGET_HOME" ] || fail "cannot determine home directory for $TARGET_USER"
  USE_SYSTEMD=0
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    USE_SYSTEMD=1
    systemctl stop anyssh-client.service 2>/dev/null || :
  fi
  stop_pid_file /run/anyssh-client.pid /usr/local/bin/anyssh-client
  mkdir -p /usr/local/bin
  cp "$TMP_FILE" /usr/local/bin/anyssh-client
  chmod 0755 /usr/local/bin/anyssh-client
  if [ "$USE_SYSTEMD" -eq 1 ]; then
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
    systemctl enable anyssh-client.service
    systemctl restart anyssh-client.service
    echo "AnySSH client installed or updated and restarted with systemd."
  else
    start_background /usr/local/bin/anyssh-client /var/log/anyssh-client.log /run/anyssh-client.pid
  fi
else
  INSTALL_DIR="$HOME/.local/share/anyssh"
  mkdir -p "$INSTALL_DIR"
  chmod 0700 "$INSTALL_DIR"
  stop_pid_file "$INSTALL_DIR/client.pid" "$INSTALL_DIR/anyssh-client"
  cp "$TMP_FILE" "$INSTALL_DIR/anyssh-client"
  chmod 0755 "$INSTALL_DIR/anyssh-client"
  start_background "$INSTALL_DIR/anyssh-client" "$INSTALL_DIR/client.log" "$INSTALL_DIR/client.pid"
fi
`, shellQuote(serverURL), checksumCases.String())
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
