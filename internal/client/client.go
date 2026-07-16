package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"anyssh/internal/protocol"
	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

type Config struct {
	ServerURL   string
	PublicURL   string
	RotateEvery time.Duration
	NotifyURL   string
	NotifyUser  string
	Secret      string
	Shell       string
	Logger      *slog.Logger
}

type Client struct {
	cfg       Config
	serverURL *url.URL
	publicURL *url.URL
	logger    *slog.Logger
	http      *http.Client
}

func New(cfg Config) (*Client, error) {
	if cfg.RotateEvery <= 0 {
		return nil, errors.New("rotate interval must be greater than zero")
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
	if cfg.NotifyURL == "" || cfg.NotifyUser == "" {
		return nil, errors.New("notify URL and user are required")
	}
	if cfg.Shell == "" {
		cfg.Shell = defaultShell()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		cfg:       cfg,
		serverURL: serverURL,
		publicURL: publicURL,
		logger:    logger,
		http:      &http.Client{Timeout: 15 * time.Second},
	}, nil
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
		cycleCtx, cancel := context.WithTimeout(ctx, c.cfg.RotateEvery)
		link := strings.TrimRight(c.publicURL.String(), "/") + "/s/" + token + "/"
		c.logger.Info("new access link", "url", link, "valid_for", c.cfg.RotateEvery)
		registered := make(chan struct{})
		go c.notifyAfterRegistration(cycleCtx, registered, link)
		c.keepRegistered(cycleCtx, token, registered)
		cancel()
	}
	return ctx.Err()
}

func (c *Client) keepRegistered(ctx context.Context, token string, registered chan struct{}) {
	backoff := time.Second
	var registeredOnce sync.Once
	for ctx.Err() == nil {
		ws, err := c.dial(ctx, "/api/register", url.Values{"token": {token}}, http.Header{
			"X-AnySSH-Secret": []string{c.cfg.Secret},
		})
		if err != nil {
			c.logger.Warn("connect to server failed", "error", err, "retry_in", backoff)
			if !wait(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		registeredOnce.Do(func() { close(registered) })
		c.logger.Info("connected to server")
		err = c.serveControl(ctx, ws)
		_ = ws.Close()
		if err != nil && ctx.Err() == nil {
			c.logger.Warn("server connection closed", "error", err)
		}
	}
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

func (c *Client) notifyAfterRegistration(ctx context.Context, registered <-chan struct{}, link string) {
	select {
	case <-ctx.Done():
		return
	case <-registered:
		c.notifyUntilSent(ctx, link)
	}
}

func (c *Client) notifyUntilSent(ctx context.Context, link string) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := c.notify(ctx, link); err == nil {
			c.logger.Info("access link sent to notification API")
			return
		} else {
			c.logger.Warn("notification failed", "error", err, "retry_in", backoff)
		}
		if !wait(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (c *Client) notify(ctx context.Context, link string) error {
	body, err := json.Marshal(struct {
		User string `json:"user"`
		Msg  string `json:"msg"`
	}{User: c.cfg.NotifyUser, Msg: link})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.NotifyURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notification API returned %s", resp.Status)
	}
	return nil
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
