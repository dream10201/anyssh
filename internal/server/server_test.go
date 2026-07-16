package server

import (
	"bytes"
	"context"
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

func TestRegistrationPageAndSessionProxy(t *testing.T) {
	t.Parallel()
	srv, err := New(Config{})
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

	want := []byte{protocol.DataInputOutput, 'i', 'd', '\n'}
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
		"od -An -tu1 -j18 -N2",
		"sha256sum -c -",
		"systemctl stop anyssh-client.service",
		"systemctl restart anyssh-client.service",
		"stop_pid_file",
		"nohup",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("installer does not contain %q", want)
		}
	}
	stopIndex := strings.Index(script, "systemctl stop anyssh-client.service")
	installIndex := strings.Index(script, `cp "$TMP_FILE" /usr/local/bin/anyssh-client`)
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
	srv, err := New(Config{PublicURL: "https://configured.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://internal:8080/install", nil)
	if got := srv.installServerURL(req); got != "https://configured.example.com" {
		t.Fatalf("configured public URL: %q", got)
	}

	srv, err = New(Config{})
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
	srv, err := New(Config{PublicURL: "http://127.0.0.1:1", ClientRotate: 10 * time.Minute})
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
	srv, err := New(Config{PublicURL: "http://ssh.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	page, err := http.Get(httpServer.URL + "/admin/")
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
	resp, err := http.Get(httpServer.URL + "/api/admin/clients")
	if err != nil {
		t.Fatal(err)
	}
	var clients []adminClient
	if json.NewDecoder(resp.Body).Decode(&clients) != nil {
		t.Fatal("decode clients")
	}
	_ = resp.Body.Close()
	if len(clients) != 1 || clients[0].Hostname != "server-a" || clients[0].Arch != "arm64" {
		t.Fatalf("clients: %+v", clients)
	}
	postJSON(t, httpServer.URL+"/api/admin/clients/device-1/disable", `{"disabled":true}`)
	linkResp, err := http.Get(httpServer.URL + "/s/" + token + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = linkResp.Body.Close()
	if linkResp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled link status %d", linkResp.StatusCode)
	}
	postJSON(t, httpServer.URL+"/api/admin/clients/device-1/disable", `{"disabled":false}`)
	postJSON(t, httpServer.URL+"/api/admin/clients/device-1/rotate", `{}`)
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
	srv, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	srv.weComEndpoint = endpoint.URL
	srv.weComKey = "test-key"
	if err := srv.postWeCom("test content"); err != nil {
		t.Fatal(err)
	}
	markdown, ok := payload["markdown"].(map[string]any)
	if payload["msgtype"] != "markdown" || !ok || markdown["content"] != "test content" {
		t.Fatalf("payload: %#v", payload)
	}
	if payload["msgtype"] != "markdown" {
		t.Fatal("unexpected message type")
	}
}

func TestSettingsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	srv, err := New(Config{DataFile: path})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(Config{DataFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.secret != srv.secret || reloaded.secret == "" {
		t.Fatalf("generated client secret was not persisted")
	}
}

func TestExplicitSecretOverridesPersistedSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, err := New(Config{DataFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if first.secret == "" {
		t.Fatal("automatic secret was not generated")
	}
	overridden, err := New(Config{DataFile: path, SharedSecret: "replacement-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.secret != "replacement-secret" {
		t.Fatal("explicit secret did not override state")
	}
	reloaded, err := New(Config{DataFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.secret != "replacement-secret" {
		t.Fatalf("override was not persisted: %q", reloaded.secret)
	}
}

func postJSON(t *testing.T, url, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: %d %s", url, resp.StatusCode, data)
	}
}
