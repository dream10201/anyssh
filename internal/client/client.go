package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"anyssh/internal/protocol"
	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

type Config struct {
	ServerURL   string
	PublicURL   string
	RotateEvery time.Duration
	Secret      string
	Shell       string
	Logger      *slog.Logger
}

type Client struct {
	cfg             Config
	device          deviceInfo
	serverURL       *url.URL
	publicURL       *url.URL
	logger          *slog.Logger
	rotation        atomic.Int64
	rotationVersion atomic.Int64
}

func New(cfg Config) (*Client, error) {
	if cfg.RotateEvery < 0 {
		return nil, errors.New("rotate interval cannot be negative")
	}
	serverURL, err := parseBaseURL(cfg.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("server URL: %w", err)
	}
	if cfg.PublicURL == "" {
		cfg.PublicURL = cfg.ServerURL
	}
	publicURL, err := parseBaseURL(cfg.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("public URL: %w", err)
	}
	if cfg.Shell == "" {
		cfg.Shell = defaultShell()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Client{
		cfg:       cfg,
		device:    detectDeviceInfo(),
		serverURL: serverURL,
		publicURL: publicURL,
		logger:    logger,
	}
	c.rotation.Store(int64(cfg.RotateEvery))
	return c, nil
}

func defaultShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	if current, err := user.Current(); err == nil {
		if output, err := exec.Command("getent", "passwd", current.Username).Output(); err == nil {
			fields := strings.Split(strings.TrimSpace(string(output)), ":")
			if len(fields) == 7 && strings.HasPrefix(fields[6], "/") {
				return fields[6]
			}
		}
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

func (c *Client) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		token, err := randomToken()
		if err != nil {
			return err
		}
		cycleCtx, cancel := context.WithCancel(ctx)
		go c.watchRotation(cycleCtx, cancel)
		rotation := time.Duration(c.rotation.Load())
		link := strings.TrimRight(c.publicURL.String(), "/") + "/s/" + token + "/"
		c.logger.Info("new access link", "url", link, "device", c.device.ID, "valid_for", rotation)
		if c.keepRegistered(cycleCtx, token) {
			cancel()
			continue
		}
		cancel()
	}
	return ctx.Err()
}

func (c *Client) watchRotation(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	last := time.Duration(c.rotation.Load())
	changedAt := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			current := time.Duration(c.rotation.Load())
			if current != last {
				last = current
				changedAt = now
			}
			if current > 0 && now.Sub(changedAt) >= current {
				cancel()
				return
			}
		}
	}
}

var errRotate = errors.New("rotate requested")

func (c *Client) keepRegistered(ctx context.Context, token string) bool {
	backoff := time.Second
	for ctx.Err() == nil {
		ws, err := c.dial(ctx, "/api/register", url.Values{"token": {token}}, http.Header{
			"X-AnySSH-Secret":          []string{c.cfg.Secret},
			"X-AnySSH-Device-ID":       []string{c.device.ID},
			"X-AnySSH-Device-Hostname": []string{c.device.Hostname},
			"X-AnySSH-Device-User":     []string{c.device.Username},
			"X-AnySSH-Device-OS":       []string{c.device.OS},
			"X-AnySSH-Device-Arch":     []string{c.device.Arch},
			"X-AnySSH-Rotate-Seconds":  []string{fmt.Sprint(c.rotation.Load() / int64(time.Second))},
			"X-AnySSH-Rotate-Version":  []string{fmt.Sprint(c.rotationVersion.Load())},
		})
		if err != nil {
			c.logger.Warn("connect to server failed", "error", err, "retry_in", backoff)
			if !wait(ctx, backoff) {
				return false
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		c.logger.Info("connected to server")
		err = c.serveControl(ctx, ws)
		_ = ws.Close()
		if errors.Is(err, errRotate) {
			return true
		}
		if err != nil && ctx.Err() == nil {
			c.logger.Warn("server connection closed", "error", err)
		}
	}
	return false
}

func (c *Client) serveControl(ctx context.Context, ws *websocket.Conn) error {
	done := make(chan error, 1)
	go func() {
		for {
			var msg protocol.ControlMessage
			if err := ws.ReadJSON(&msg); err != nil {
				done <- err
				return
			}
			if msg.Type == "open" && msg.SessionID != "" && msg.Key != "" {
				go c.runSession(ctx, msg.SessionID, msg.Key)
			} else if msg.Type == "rotate" {
				done <- errRotate
				return
			} else if msg.Type == "set_rotate" && msg.RotateSeconds >= 0 && msg.RotateVersion >= c.rotationVersion.Load() {
				c.rotation.Store(msg.RotateSeconds * int64(time.Second))
				c.rotationVersion.Store(msg.RotateVersion)
				c.logger.Info("link rotation updated", "interval", time.Duration(c.rotation.Load()))
			}
		}
	}()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotating"), time.Now().Add(time.Second))
			return ctx.Err()
		case err := <-done:
			return err
		case <-ticker.C:
			if err := ws.WriteJSON(protocol.ControlMessage{Type: "heartbeat"}); err != nil {
				return err
			}
		}
	}
}

func (c *Client) runSession(ctx context.Context, id, key string) {
	ws, err := c.dial(ctx, "/api/session", url.Values{"id": {id}}, http.Header{
		"X-AnySSH-Session-Key": []string{key},
	})
	if err != nil {
		c.logger.Warn("open terminal transport failed", "error", err)
		return
	}
	defer ws.Close()

	cmd := loginShellCommand(ctx, c.cfg.Shell)
	cmd.Env = loginEnvironment(c.cfg.Shell)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		c.logger.Warn("start shell failed", "error", err)
		return
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	var writeMu sync.Mutex
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		defer ws.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				frame := append([]byte{protocol.DataInputOutput}, buf[:n]...)
				writeMu.Lock()
				writeErr := ws.WriteMessage(websocket.BinaryMessage, frame)
				writeMu.Unlock()
				if writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		kind, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if kind != websocket.BinaryMessage || len(data) == 0 {
			continue
		}
		switch data[0] {
		case protocol.DataInputOutput:
			if _, err := ptmx.Write(data[1:]); err != nil {
				return
			}
		case protocol.DataResize:
			var size protocol.Resize
			if json.Unmarshal(data[1:], &size) == nil && size.Cols > 0 && size.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: size.Cols, Rows: size.Rows})
			}
		}
		select {
		case <-closed:
			return
		default:
		}
	}
}

func loginShellCommand(ctx context.Context, shell string) *exec.Cmd {
	switch filepath.Base(shell) {
	case "bash":
		return exec.CommandContext(ctx, shell, "--login", "-i")
	case "zsh":
		return exec.CommandContext(ctx, shell, "-l", "-i")
	case "fish":
		return exec.CommandContext(ctx, shell, "--login", "--interactive")
	default:
		return exec.CommandContext(ctx, shell, "-l")
	}
}

func loginEnvironment(shell string) []string {
	env := make(map[string]string)
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			env[key] = value
		}
	}
	if current, err := user.Current(); err == nil {
		env["HOME"] = current.HomeDir
		env["USER"] = current.Username
		env["LOGNAME"] = current.Username
	}
	env["SHELL"] = shell
	env["TERM"] = "xterm-256color"
	env["COLORTERM"] = "truecolor"
	if env["PATH"] == "" {
		env["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	result := make([]string, 0, len(env))
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	return result
}

func (c *Client) dial(ctx context.Context, path string, query url.Values, header http.Header) (*websocket.Conn, error) {
	u := *c.serverURL
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = query.Encode()
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	return ws, err
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must be an http(s) URL such as http://1.2.3.4:8080")
	}
	return u, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
