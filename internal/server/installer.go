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

var clientArchitectures = []string{"amd64", "arm64", "arm"}

func (s *Server) handleClientDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	arch := strings.TrimPrefix(r.URL.Path, "/download/anyssh-client/")
	if r.URL.Path == "/download/anyssh-client" || !validClientArchitecture(arch) {
		http.Error(w, "client architecture must be amd64, arm64, or arm", http.StatusBadRequest)
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
	script := renderInstallScript(
		serverURL,
		checksums["amd64"],
		checksums["arm64"],
		checksums["arm"],
	)
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

func renderInstallScript(serverURL, checksumAMD64, checksumARM64, checksumARM string) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

SERVER_URL=%s
SHA256_AMD64=%s
SHA256_ARM64=%s
SHA256_ARM=%s

fail() { echo "anyssh install: $*" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"

case "$(uname -s)" in Linux) ;; *) fail "only Linux clients are supported" ;; esac
case "$(uname -m)" in
  x86_64|amd64) ACTUAL_ARCH=amd64; EXPECTED_SHA256="$SHA256_AMD64" ;;
  aarch64|arm64) ACTUAL_ARCH=arm64; EXPECTED_SHA256="$SHA256_ARM64" ;;
  arm|armv5l|armv6l|armv7l|armhf) ACTUAL_ARCH=arm; EXPECTED_SHA256="$SHA256_ARM" ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
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
`, shellQuote(serverURL), shellQuote(checksumAMD64), shellQuote(checksumARM64), shellQuote(checksumARM))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
