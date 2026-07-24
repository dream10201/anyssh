package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"anyssh/internal/bootstrap"
	"anyssh/internal/protocol"
	"github.com/gorilla/websocket"
)

func testConfig(cfg Config) Config {
	if cfg.SharedSecret == "" {
		cfg.SharedSecret = "test-secret"
	}
	return cfg
}

func TestRegistrationPageAndSessionProxy(t *testing.T) {
	t.Parallel()
	srv, err := New(testConfig(Config{}))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	wsBase := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	token := strings.Repeat("a", 64)

	registerHeader := http.Header{"X-AnySSH-Secret": []string{srv.secret}}
	control, _, err := websocket.DefaultDialer.Dial(wsBase+"/api/register?token="+token, registerHeader)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer control.Close()
	var initial protocol.ControlMessage
	if control.ReadJSON(&initial) != nil || initial.Type != "set_rotate" {
		t.Fatalf("initial rotation message: %+v", initial)
	}

	pageResp, err := http.Get(httpServer.URL + "/s/" + token + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(pageResp.Body)
	_ = pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK || !bytes.Contains(page, []byte("AnySSH Terminal")) {
		t.Fatalf("unexpected page response: status=%d body=%q", pageResp.StatusCode, page)
	}
	assetResp, err := http.Get(httpServer.URL + "/s/" + token + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = assetResp.Body.Close()
	if assetResp.StatusCode != http.StatusOK {
		t.Fatalf("asset status: %d", assetResp.StatusCode)
	}

	browser, _, err := websocket.DefaultDialer.Dial(wsBase+"/s/"+token+"/ws", nil)
	if err != nil {
		t.Fatalf("browser websocket: %v", err)
	}
	defer browser.Close()
	var open protocol.ControlMessage
	if err := control.ReadJSON(&open); err != nil {
		t.Fatalf("read open request: %v", err)
	}
	if open.Type != "open" || open.SessionID == "" || open.Key == "" {
		t.Fatalf("invalid open request: %+v", open)
	}
	header := http.Header{"X-AnySSH-Session-Key": []string{open.Key}}
	remote, _, err := websocket.DefaultDialer.Dial(wsBase+"/api/session?id="+open.SessionID, header)
	if err != nil {
		t.Fatalf("terminal websocket: %v", err)
	}
	defer remote.Close()

	want := append([]byte{protocol.DataInputOutput}, []byte("echo 中文\n")...)
	if err := browser.WriteMessage(websocket.BinaryMessage, want); err != nil {
		t.Fatal(err)
	}
	kind, got, err := remote.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.BinaryMessage || !bytes.Equal(got, want) {
		t.Fatalf("proxy got kind=%d data=%q", kind, got)
	}
}

func TestSharedSecretAndExpiredLink(t *testing.T) {
	t.Parallel()
	srv, err := New(Config{SharedSecret: "server-secret"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	token := strings.Repeat("b", 64)
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/register?token=" + token

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, err=%v status=%v", err, responseStatus(resp))
	}
	_ = resp.Body.Close()

	header := http.Header{"X-AnySSH-Secret": []string{"server-secret"}}
	control, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	_ = control.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(httpServer.URL + "/s/" + token + "/")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("link remained active with status %d", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResizeJSONShape(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(protocol.Resize{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"cols":120,"rows":40}` {
		t.Fatalf("unexpected resize JSON: %s", data)
	}
}

func responseStatus(resp *http.Response) any {
	if resp == nil {
		return nil
	}
	return resp.StatusCode
}

func TestInstallScript(t *testing.T) {
	t.Parallel()
	checksums := make(map[string]string, len(clientArchitectures))
	for _, arch := range clientArchitectures {
		checksums[arch] = strings.Repeat("a", 64)
	}
	script := renderInstallScript("https://ssh.example.com", checksums)
	for _, want := range []string{
		"#!/bin/sh",
		"SERVER_URL='https://ssh.example.com'",
		"wget -qO",
		"openssl dgst -sha256",
		"installing without download verification",
		"download/anyssh-client/$ACTUAL_ARCH",
		"armv5*|armv6*|armv7*",
		"command -v arch",
		"busybox uname -m",
		"dpkg --print-architecture",
		"rpm --eval '%{_arch}'",
		"apk --print-arch",
		"getconf \"$key\"",
		"/proc/cpuinfo",
		"elf_arch",
		"$OD -An -tu1 -j18 -N2",
		"sha256sum -c -",
		"systemctl stop anyssh-client.service",
		"systemctl restart anyssh-client.service",
		"stop_pid_file",
		"nohup",
		"using plain background mode",
		"busybox cp",
		"busybox chmod",
		"busybox mkdir",
		"/proc/self/status",
		"systemd start failed; using background mode",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("installer does not contain %q", want)
		}
	}
	stopIndex := strings.Index(script, "systemctl stop anyssh-client.service")
	installIndex := strings.Index(script, `install_file "$TMP_FILE" /usr/local/bin/anyssh-client`)
	restartIndex := strings.Index(script, "systemctl restart anyssh-client.service")
	if stopIndex < 0 || installIndex <= stopIndex || restartIndex <= installIndex {
		t.Fatal("systemd update must stop, replace, then restart")
	}
	for _, arch := range clientArchitectures {
		if !strings.Contains(script, "  "+arch+") EXPECTED_SHA256=") {
			t.Errorf("installer is missing checksum branch for %s", arch)
		}
	}
	for _, unwanted := range []string{"CONFIG_BASE64", "/etc/anyssh", ".config/anyssh"} {
		if strings.Contains(script, unwanted) {
			t.Fatalf("installer unexpectedly contains %q", unwanted)
		}
	}
	for _, shell := range []string{"/bin/sh", "/usr/bin/dash"} {
		cmd := exec.Command(shell, "-n")
		cmd.Stdin = strings.NewReader(script)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("invalid installer syntax for %s: %v\n%s", shell, err, output)
		}
	}
}

func TestArchitectureDetectionWithoutUname(t *testing.T) {
	t.Parallel()
	checksums := make(map[string]string, len(clientArchitectures))
	for _, arch := range clientArchitectures {
		checksums[arch] = strings.Repeat("a", 64)
	}
	script := renderInstallScript("https://ssh.example.com", checksums)
	start := strings.Index(script, "normalize_arch() {")
	end := strings.Index(script, "if command -v mktemp")
	if start < 0 || end <= start {
		t.Fatal("cannot locate architecture detector in installer")
	}
	detector := script[start:end] + "\nprintf '%s\\n' \"$ACTUAL_ARCH\"\n"

	t.Run("dpkg", func(t *testing.T) {
		binDir := t.TempDir()
		writeExecutable(t, filepath.Join(binDir, "dpkg"), "#!/bin/sh\necho riscv64\n")
		cmd := exec.Command("/bin/sh", "-c", detector)
		cmd.Env = []string{"PATH=" + binDir}
		output, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "riscv64" {
			t.Fatalf("dpkg fallback: err=%v output=%q", err, output)
		}
	})

	t.Run("elf", func(t *testing.T) {
		binDir := t.TempDir()
		writeExecutable(t, filepath.Join(binDir, "od"), `#!/bin/sh
case "$*" in
  *-j4*) echo "2 1" ;;
  *-j18*) echo "62 0" ;;
esac
`)
		cmd := exec.Command("/bin/sh", "-c", detector)
		cmd.Env = []string{"PATH=" + binDir}
		output, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "amd64" {
			t.Fatalf("ELF fallback: err=%v output=%q", err, output)
		}
	})
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestInstallServerURL(t *testing.T) {
	t.Parallel()
	srv, err := New(testConfig(Config{PublicURL: "https://configured.example.com/"}))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://internal:8080/install", nil)
	if got := srv.installServerURL(req); got != "https://configured.example.com" {
		t.Fatalf("configured public URL: %q", got)
	}

	srv, err = New(testConfig(Config{}))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "ssh.example.com")
	if got := srv.installServerURL(req); got != "https://ssh.example.com" {
		t.Fatalf("forwarded public URL: %q", got)
	}
}

func TestConfiguredClientTrailer(t *testing.T) {
	t.Parallel()
	srv, err := New(Config{PublicURL: "https://ssh.example.com", SharedSecret: "secret", ClientRotate: 20 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://internal/download/anyssh-client/amd64", nil)
	for _, arch := range clientArchitectures {
		binary, err := srv.configuredClient(req, arch)
		if err != nil {
			t.Fatalf("architecture %s: %v", arch, err)
		}
		cfg, found, err := bootstrap.Parse(bytes.NewReader(binary), int64(len(binary)))
		if err != nil {
			t.Fatalf("architecture %s: %v", arch, err)
		}
		if !found || cfg.ServerURL != "https://ssh.example.com" || cfg.Secret != "secret" || cfg.Rotate != "20m0s" {
			t.Fatalf("architecture %s: unexpected trailer: found=%v config=%+v", arch, found, cfg)
		}
	}
}

func TestConfiguredClientIsExecutable(t *testing.T) {
	if runtime.GOOS != "linux" || !validClientArchitecture(runtime.GOARCH) {
		t.Skip("ELF trailer execution test requires a supported Linux client")
	}
	srv, err := New(testConfig(Config{PublicURL: "http://127.0.0.1:1", ClientRotate: 10 * time.Minute}))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://internal/download/anyssh-client/"+runtime.GOARCH, nil)
	binary, err := srv.configuredClient(req, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "anyssh-client")
	if err := os.WriteFile(path, binary, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	output, _ := exec.CommandContext(ctx, path).CombinedOutput()
	if !bytes.Contains(output, []byte("new access link")) {
		t.Fatalf("configured client did not start without parameters: %s", output)
	}
}

func TestAdminClientControls(t *testing.T) {
	srv, err := New(testConfig(Config{PublicURL: "http://ssh.example.com", AdminSecret: "admin-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	unauthorized, err := http.Get(httpServer.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin without secret: %d", unauthorized.StatusCode)
	}
	page, err := adminRequest(http.MethodGet, httpServer.URL+"/admin/", "", srv.adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	if page.StatusCode != http.StatusOK || !bytes.Contains(data, []byte("AnySSH")) {
		t.Fatalf("admin page: %d %s", page.StatusCode, data)
	}
	token := strings.Repeat("c", 64)
	headers := http.Header{"X-AnySSH-Device-ID": []string{"device-1"}, "X-AnySSH-Device-Hostname": []string{"server-a"}, "X-AnySSH-Device-User": []string{"deploy"}, "X-AnySSH-Device-OS": []string{"linux"}, "X-AnySSH-Device-Arch": []string{"arm64"}}
	headers.Set("X-AnySSH-Secret", srv.secret)
	control, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/register?token="+token, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	var initial protocol.ControlMessage
	if control.ReadJSON(&initial) != nil || initial.Type != "set_rotate" {
		t.Fatalf("initial rotation message: %+v", initial)
	}
	putAdminJSON(t, http.MethodPost, httpServer.URL+"/api/admin/clients/device-1/rotation", `{"rotate_seconds":120}`, srv.adminSecret)
	var update protocol.ControlMessage
	if control.ReadJSON(&update) != nil || update.Type != "set_rotate" || update.RotateSeconds != 120 || update.RotateVersion <= 0 {
		t.Fatalf("rotation update: %+v", update)
	}
	resp, err := adminRequest(http.MethodGet, httpServer.URL+"/api/admin/clients", "", srv.adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	var clients []adminClient
	if json.NewDecoder(resp.Body).Decode(&clients) != nil {
		t.Fatal("decode clients")
	}
	_ = resp.Body.Close()
	if len(clients) != 1 || clients[0].Hostname != "server-a" || clients[0].Arch != "arm64" || clients[0].RotateSeconds != 120 {
		t.Fatalf("clients: %+v", clients)
	}
	putAdminJSON(t, http.MethodPost, httpServer.URL+"/api/admin/clients/device-1/disable", `{"disabled":true}`, srv.adminSecret)
	linkResp, err := http.Get(httpServer.URL + "/s/" + token + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = linkResp.Body.Close()
	if linkResp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled link status %d", linkResp.StatusCode)
	}
	putAdminJSON(t, http.MethodPost, httpServer.URL+"/api/admin/clients/device-1/disable", `{"disabled":false}`, srv.adminSecret)
	putAdminJSON(t, http.MethodPost, httpServer.URL+"/api/admin/clients/device-1/rotate", `{}`, srv.adminSecret)
	var message protocol.ControlMessage
	if control.ReadJSON(&message) != nil || message.Type != "rotate" {
		t.Fatalf("rotate message: %+v", message)
	}
}

func TestWeComWebhookPayload(t *testing.T) {
	var payload map[string]any
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer endpoint.Close()
	srv, err := New(testConfig(Config{}))
	if err != nil {
		t.Fatal(err)
	}
	srv.weComEndpoint = endpoint.URL
	srv.weComKey = "test-key"
	if err := srv.postWeCom("test content"); err != nil {
		t.Fatal(err)
	}
	text, ok := payload["text"].(map[string]any)
	if payload["msgtype"] != "text" || !ok || text["content"] != "test content" {
		t.Fatalf("payload: %#v", payload)
	}
	if _, exists := payload["markdown"]; exists {
		t.Fatal("unexpected markdown payload")
	}
}

func TestSecretIsRequiredAndPermanentRotationAllowed(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected missing secret to be rejected")
	}
	srv, err := New(Config{SharedSecret: "secret", ClientRotate: 0})
	if err != nil {
		t.Fatal(err)
	}
	if srv.clientRotate != 0 {
		t.Fatalf("rotation=%s, want permanent", srv.clientRotate)
	}
}

func TestClientRotationIsRecoveredFromReconnectingClient(t *testing.T) {
	srv, err := New(Config{SharedSecret: "secret", ClientRotate: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	token := strings.Repeat("d", 64)
	header := http.Header{"X-AnySSH-Secret": []string{"secret"}, "X-AnySSH-Rotate-Seconds": []string{"0"}, "X-AnySSH-Rotate-Version": []string{"12345"}}
	control, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/register?token="+token, header)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	var message protocol.ControlMessage
	if control.ReadJSON(&message) != nil {
		t.Fatal("read rotation")
	}
	if message.RotateSeconds != 0 || message.RotateVersion != 12345 {
		t.Fatalf("recovered message: %+v", message)
	}
	resp, err := adminRequest(http.MethodGet, httpServer.URL+"/api/admin/clients", "", srv.adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var clients []adminClient
	if json.NewDecoder(resp.Body).Decode(&clients) != nil {
		t.Fatal("decode clients")
	}
	if len(clients) != 1 || clients[0].RotateSeconds != 0 {
		t.Fatalf("clients: %+v", clients)
	}
}

func TestClientRotationUpdateIsNotBroadcast(t *testing.T) {
	srv, err := New(Config{SharedSecret: "secret", AdminSecret: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/register?token="
	connect := func(tokenByte, deviceID string) *websocket.Conn {
		header := http.Header{
			"X-AnySSH-Secret":         []string{"secret"},
			"X-AnySSH-Device-ID":      []string{deviceID},
			"X-AnySSH-Rotate-Seconds": []string{"60"},
			"X-AnySSH-Rotate-Version": []string{"1"},
		}
		conn, _, dialErr := websocket.DefaultDialer.Dial(wsURL+strings.Repeat(tokenByte, 64), header)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		var initial protocol.ControlMessage
		if conn.ReadJSON(&initial) != nil || initial.RotateSeconds != 60 {
			t.Fatalf("initial rotation for %s: %+v", deviceID, initial)
		}
		return conn
	}
	first := connect("e", "device-1")
	defer first.Close()
	second := connect("f", "device-2")
	defer second.Close()

	putAdminJSON(t, http.MethodPost, httpServer.URL+"/api/admin/clients/device-1/rotation", `{"rotate_seconds":300}`, srv.adminSecret)
	var update protocol.ControlMessage
	if first.ReadJSON(&update) != nil || update.Type != "set_rotate" || update.RotateSeconds != 300 || update.RotateVersion <= 1 {
		t.Fatalf("target rotation update: %+v", update)
	}
	_ = second.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if err := second.ReadJSON(&update); err == nil {
		t.Fatalf("rotation update was broadcast to device-2: %+v", update)
	}
}

func TestClientRotationSurvivesClientReconnect(t *testing.T) {
	srv, err := New(Config{SharedSecret: "secret", AdminSecret: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/register?token="
	header := http.Header{
		"X-AnySSH-Secret":         []string{"secret"},
		"X-AnySSH-Device-ID":      []string{"device-1"},
		"X-AnySSH-Rotate-Seconds": []string{"60"},
		"X-AnySSH-Rotate-Version": []string{"0"},
	}
	first, _, err := websocket.DefaultDialer.Dial(wsURL+strings.Repeat("a", 64), header)
	if err != nil {
		t.Fatal(err)
	}
	var message protocol.ControlMessage
	if first.ReadJSON(&message) != nil || message.RotateSeconds != 60 {
		t.Fatalf("initial rotation: %+v", message)
	}
	putAdminJSON(t, http.MethodPost, httpServer.URL+"/api/admin/clients/device-1/rotation", `{"rotate_seconds":300}`, srv.adminSecret)
	if first.ReadJSON(&message) != nil || message.RotateSeconds != 300 {
		t.Fatalf("rotation update: %+v", message)
	}
	_ = first.Close()

	second, _, err := websocket.DefaultDialer.Dial(wsURL+strings.Repeat("b", 64), header)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.ReadJSON(&message) != nil || message.RotateSeconds != 300 {
		t.Fatalf("rotation after reconnect: %+v", message)
	}
}

func TestDeviceNoteFlow(t *testing.T) {
	srv, err := New(Config{SharedSecret: "secret", AdminSecret: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/register?token="
	header := http.Header{
		"X-AnySSH-Secret":    []string{"secret"},
		"X-AnySSH-Device-ID": []string{"device-1"},
	}
	control, _, err := websocket.DefaultDialer.Dial(wsURL+strings.Repeat("a", 64), header)
	if err != nil {
		t.Fatal(err)
	}
	var msg protocol.ControlMessage
	if control.ReadJSON(&msg) != nil || msg.Type != "set_rotate" {
		t.Fatalf("initial message: %+v", msg)
	}

	putAdminJSON(t, http.MethodPost, httpServer.URL+"/api/admin/clients/device-1/note", `{"note":"web-01 生产环境"}`, srv.adminSecret)
	if control.ReadJSON(&msg) != nil || msg.Type != "set_note" || msg.Note != "web-01 生产环境" || msg.NoteVersion <= 0 {
		t.Fatalf("note update: %+v", msg)
	}

	resp, err := adminRequest(http.MethodGet, httpServer.URL+"/api/admin/clients", "", srv.adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	var clients []adminClient
	if json.NewDecoder(resp.Body).Decode(&clients) != nil {
		t.Fatal("decode clients")
	}
	_ = resp.Body.Close()
	if len(clients) != 1 || clients[0].Note != "web-01 生产环境" {
		t.Fatalf("clients note: %+v", clients)
	}
	_ = control.Close()

	// A restarted client reports version 0; the server-remembered note wins.
	second, _, err := websocket.DefaultDialer.Dial(wsURL+strings.Repeat("b", 64), header)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.ReadJSON(&msg) != nil || msg.Type != "set_rotate" {
		t.Fatalf("reconnect rotate: %+v", msg)
	}
	if second.ReadJSON(&msg) != nil || msg.Type != "set_note" || msg.Note != "web-01 生产环境" {
		t.Fatalf("reconnect note: %+v", msg)
	}
}

func TestClientReportedNoteWinsWithHigherVersion(t *testing.T) {
	srv, err := New(Config{SharedSecret: "secret", AdminSecret: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/register?token="
	header := http.Header{
		"X-AnySSH-Secret":       []string{"secret"},
		"X-AnySSH-Device-ID":    []string{"device-1"},
		"X-AnySSH-Note":         []string{base64.StdEncoding.EncodeToString([]byte("client note"))},
		"X-AnySSH-Note-Version": []string{"5"},
	}
	control, _, err := websocket.DefaultDialer.Dial(wsURL+strings.Repeat("a", 64), header)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	resp, err := adminRequest(http.MethodGet, httpServer.URL+"/api/admin/clients", "", srv.adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	var clients []adminClient
	if json.NewDecoder(resp.Body).Decode(&clients) != nil {
		t.Fatal("decode clients")
	}
	_ = resp.Body.Close()
	if len(clients) != 1 || clients[0].Note != "client note" {
		t.Fatalf("client-reported note not adopted: %+v", clients)
	}
}

func TestAdminClientsAreSortedByHostname(t *testing.T) {
	srv, err := New(Config{SharedSecret: "secret", AdminSecret: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/register?token="
	register := func(token, id, hostname string) *websocket.Conn {
		header := http.Header{
			"X-AnySSH-Secret":          []string{"secret"},
			"X-AnySSH-Device-ID":       []string{id},
			"X-AnySSH-Device-Hostname": []string{hostname},
		}
		conn, _, dialErr := websocket.DefaultDialer.Dial(wsURL+token, header)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		return conn
	}
	z := register(strings.Repeat("a", 64), "device-z", "zeta")
	defer z.Close()
	a := register(strings.Repeat("b", 64), "device-a", "alpha")
	defer a.Close()

	var clients []adminClient
	// Poll briefly so both registrations are visible regardless of goroutine timing.
	for attempt := 0; attempt < 20; attempt++ {
		resp, reqErr := adminRequest(http.MethodGet, httpServer.URL+"/api/admin/clients", "", srv.adminSecret)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		clients = nil
		if json.NewDecoder(resp.Body).Decode(&clients) != nil {
			t.Fatal("decode clients")
		}
		_ = resp.Body.Close()
		if len(clients) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(clients) != 2 || clients[0].Hostname != "alpha" || clients[1].Hostname != "zeta" {
		t.Fatalf("expected hostname-sorted clients, got %+v", clients)
	}
}

func TestMalformedClientDataDoesNotCrashServer(t *testing.T) {
	srv, err := New(Config{SharedSecret: "secret", AdminSecret: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	base := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	// 1) Invalid tokens must be rejected before any upgrade, never panic.
	for _, tok := range []string{"", "short", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		conn, resp, dialErr := websocket.DefaultDialer.Dial(base+"/api/register?token="+tok, http.Header{"X-AnySSH-Secret": {"secret"}})
		if dialErr == nil {
			conn.Close()
			t.Fatalf("expected rejection for token %q", tok)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
	}

	// 2) A valid token with deliberately garbage headers must be tolerated.
	oversizedNote := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("中", 600))) // 1800 bytes -> truncated mid-rune
	junk := http.Header{
		"X-AnySSH-Secret":          {"secret"},
		"X-AnySSH-Device-ID":       {strings.Repeat("x", 5000)},
		"X-AnySSH-Device-Hostname": {strings.Repeat("主机名", 200)},
		"X-AnySSH-Rotate-Seconds":  {"not-a-number"},
		"X-AnySSH-Rotate-Version":  {"-999"},
		"X-AnySSH-Note":            {"!!!not-valid-base64!!!"},
		"X-AnySSH-Note-Version":    {"nope"},
	}
	conn, _, err := websocket.DefaultDialer.Dial(base+"/api/register?token="+strings.Repeat("a", 64), junk)
	if err != nil {
		t.Fatalf("register with junk headers failed: %v", err)
	}
	var msg protocol.ControlMessage
	if conn.ReadJSON(&msg) != nil || msg.Type != "set_rotate" {
		t.Fatalf("expected set_rotate after junk register, got %+v", msg)
	}
	// Junk on the control channel must not crash the discard loop.
	_ = conn.WriteMessage(websocket.BinaryMessage, bytes.Repeat([]byte{0xff}, 8192))
	_ = conn.WriteMessage(websocket.TextMessage, []byte("{garbage json"))
	_ = conn.Close()

	// 3) A registration carrying an oversized (valid-base64) note must be stored
	//    without breaking the admin JSON response.
	conn2, _, err := websocket.DefaultDialer.Dial(base+"/api/register?token="+strings.Repeat("b", 64),
		http.Header{"X-AnySSH-Secret": {"secret"}, "X-AnySSH-Device-ID": {"dev-note"}, "X-AnySSH-Note": {oversizedNote}, "X-AnySSH-Note-Version": {"3"}})
	if err != nil {
		t.Fatalf("register with oversized note failed: %v", err)
	}
	defer conn2.Close()
	resp, err := adminRequest(http.MethodGet, httpServer.URL+"/api/admin/clients", "", srv.adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	var clients []adminClient
	if json.NewDecoder(resp.Body).Decode(&clients) != nil {
		t.Fatal("admin clients JSON did not decode after oversized note")
	}
	_ = resp.Body.Close()

	// 4) A client session with an unknown id must be rejected, not panic.
	sconn, sresp, serr := websocket.DefaultDialer.Dial(base+"/api/session?id=deadbeef", http.Header{"X-AnySSH-Session-Key": {"whatever"}})
	if serr == nil {
		sconn.Close()
		t.Fatal("expected rejection for unknown session id")
	}
	if sresp != nil {
		_ = sresp.Body.Close()
	}

	// 5) The server must still be healthy after all of the above.
	health, err := http.Get(httpServer.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(health.Body)
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") {
		t.Fatalf("server unhealthy after malformed input: %d %s", health.StatusCode, body)
	}
}

func putAdminJSON(t *testing.T, method, url, body, secret string) {
	t.Helper()
	resp, err := adminRequest(method, url, body, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: %d %s", method, url, resp.StatusCode, data)
	}
}

func adminRequest(method, url, body, secret string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AnySSH-Admin-Secret", secret)
	return http.DefaultClient.Do(req)
}
