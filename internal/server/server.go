package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"anyssh/internal/protocol"
	"github.com/gorilla/websocket"
)

//go:embed web/* admin/* assets/*
var webFiles embed.FS

type Config struct {
	SharedSecret string
	WeComKey     string
	PublicURL    string
	ClientRotate time.Duration
	DataFile     string
	Logger       *slog.Logger
}

type Server struct {
	secret        string
	publicURL     string
	clientRotate  time.Duration
	weComKey      string
	weComEndpoint string
	dataFile      string
	logger        *slog.Logger
	upgrader      websocket.Upgrader

	mu      sync.Mutex
	clients map[string]*clientConn
	pending map[string]*pendingSession
	web     http.Handler
	admin   http.Handler
}

type clientConn struct {
	token        string
	deviceID     string
	hostname     string
	username     string
	osName       string
	arch         string
	link         string
	registeredAt time.Time
	disabled     bool
	expiresAt    time.Time
	ws           *websocket.Conn

	writeMu sync.Mutex
	mu      sync.Mutex
	closed  bool
	sockets map[*websocket.Conn]struct{}
}

type pendingSession struct {
	client *clientConn
	key    string
	ready  chan *websocket.Conn
}

func New(cfg Config) (*Server, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ClientRotate <= 0 {
		cfg.ClientRotate = time.Hour
	}
	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}
	adminAssets, err := fs.Sub(webFiles, "admin")
	if err != nil {
		return nil, err
	}
	s := &Server{
		secret:        cfg.SharedSecret,
		weComKey:      cfg.WeComKey,
		weComEndpoint: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send",
		publicURL:     strings.TrimRight(cfg.PublicURL, "/"),
		clientRotate:  cfg.ClientRotate,
		dataFile:      cfg.DataFile,
		logger:        logger,
		clients:       make(map[string]*clientConn),
		pending:       make(map[string]*pendingSession),
	}
	if err := s.loadSettings(); err != nil {
		return nil, err
	}
	if s.secret == "" {
		s.secret, err = randomHex(32)
		if err != nil {
			return nil, err
		}
		if err := s.saveSettings(); err != nil {
			return nil, err
		}
	} else if cfg.SharedSecret != "" {
		if err := s.saveSettings(); err != nil {
			return nil, err
		}
	}
	if s.publicURL != "" {
		u, err := url.Parse(s.publicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return nil, errors.New("public URL must be an http(s) origin such as https://ssh.example.com")
		}
	}
	s.web = http.FileServer(http.FS(assets))
	s.admin = http.FileServer(http.FS(adminAssets))
	s.upgrader = websocket.Upgrader{CheckOrigin: sameOrigin}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/register", s.handleRegister)
	mux.HandleFunc("/api/session", s.handleClientSession)
	mux.HandleFunc("/download/anyssh-client", s.handleClientDownload)
	mux.HandleFunc("/download/anyssh-client/", s.handleClientDownload)
	mux.HandleFunc("/install", s.handleInstall)
	mux.HandleFunc("/admin/", s.handleAdminPage)
	mux.HandleFunc("/api/admin/clients", s.handleAdminClients)
	mux.HandleFunc("/api/admin/clients/", s.handleAdminClientAction)
	mux.HandleFunc("/s/", s.handlePublic)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	return securityHeaders(mux)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.secret != "" && !secureEqual(r.Header.Get("X-AnySSH-Secret"), s.secret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := r.URL.Query().Get("token")
	if !validToken(token) {
		http.Error(w, "invalid token", http.StatusBadRequest)
		return
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &clientConn{
		token: token, deviceID: cleanHeader(r.Header.Get("X-AnySSH-Device-ID"), token[:16]),
		hostname:     cleanHeader(r.Header.Get("X-AnySSH-Device-Hostname"), "unknown"),
		username:     cleanHeader(r.Header.Get("X-AnySSH-Device-User"), "unknown"),
		osName:       cleanHeader(r.Header.Get("X-AnySSH-Device-OS"), "unknown"),
		arch:         cleanHeader(r.Header.Get("X-AnySSH-Device-Arch"), "unknown"),
		link:         strings.TrimRight(s.installServerURL(r), "/") + "/s/" + token + "/",
		registeredAt: time.Now(), ws: ws, sockets: make(map[*websocket.Conn]struct{}),
	}

	s.mu.Lock()
	old := s.clients[token]
	s.clients[token] = c
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	s.logger.Info("client registered", "token_prefix", token[:8])
	go s.notifyClientLink(c)

	ws.SetReadLimit(4096)
	_ = ws.SetReadDeadline(time.Now().Add(70 * time.Second))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(70 * time.Second))
	})
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			break
		}
		_ = ws.SetReadDeadline(time.Now().Add(70 * time.Second))
	}
	s.removeClient(c)
}

func (s *Server) handlePublic(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/s/"), "/")
	if len(parts) == 0 || !validToken(parts[0]) {
		http.NotFound(w, r)
		return
	}
	token := parts[0]
	if len(parts) == 2 && parts[1] == "ws" {
		s.handleBrowserSession(w, r, token)
		return
	}
	if len(parts) == 1 {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusTemporaryRedirect)
		return
	}
	if c := s.getClient(token); c == nil || !c.available() {
		http.Error(w, "link expired or client offline", http.StatusNotFound)
		return
	}
	assetPath := strings.Join(parts[1:], "/")
	if assetPath == "" {
		r.URL.Path = "/"
	} else {
		r.URL.Path = "/" + assetPath
	}
	s.web.ServeHTTP(w, r)
}

func (s *Server) handleBrowserSession(w http.ResponseWriter, r *http.Request, token string) {
	c := s.getClient(token)
	if c == nil || !c.available() {
		http.Error(w, "link expired or client offline", http.StatusNotFound)
		return
	}
	browser, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browser.Close()

	id, err := randomHex(16)
	if err != nil {
		return
	}
	key, err := randomHex(32)
	if err != nil {
		return
	}
	p := &pendingSession{client: c, key: key, ready: make(chan *websocket.Conn, 1)}
	s.mu.Lock()
	if s.clients[token] != c {
		s.mu.Unlock()
		return
	}
	s.pending[id] = p
	s.mu.Unlock()
	defer s.deletePending(id, p)

	if err := c.writeJSON(protocol.ControlMessage{Type: "open", SessionID: id, Key: key}); err != nil {
		return
	}
	var remote *websocket.Conn
	select {
	case remote = <-p.ready:
	case <-time.After(10 * time.Second):
		_ = browser.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "client did not respond"), time.Now().Add(time.Second))
		return
	}
	if remote == nil {
		return
	}
	defer remote.Close()
	if !c.track(remote, true) {
		return
	}
	defer c.track(remote, false)
	proxyWebSockets(browser, remote)
}

func (s *Server) handleClientSession(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	key := r.Header.Get("X-AnySSH-Session-Key")
	s.mu.Lock()
	p := s.pending[id]
	if p != nil && secureEqual(key, p.key) {
		delete(s.pending, id)
	} else {
		p = nil
	}
	s.mu.Unlock()
	if p == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	select {
	case p.ready <- ws:
	default:
		_ = ws.Close()
	}
}

func (s *Server) getClient(token string) *clientConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clients[token]
}

func (s *Server) removeClient(c *clientConn) {
	s.mu.Lock()
	if s.clients[c.token] == c {
		delete(s.clients, c.token)
	}
	for id, p := range s.pending {
		if p.client == c {
			delete(s.pending, id)
			close(p.ready)
		}
	}
	s.mu.Unlock()
	c.close()
	s.logger.Info("client disconnected", "token_prefix", c.token[:8])
}

func (s *Server) deletePending(id string, want *pendingSession) {
	s.mu.Lock()
	if s.pending[id] == want {
		delete(s.pending, id)
	}
	s.mu.Unlock()
}

func (c *clientConn) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteJSON(v)
}

func (c *clientConn) track(ws *websocket.Conn, add bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if add && !c.closed {
		c.sockets[ws] = struct{}{}
		return true
	} else if !add {
		delete(c.sockets, ws)
	}
	return false
}

func (c *clientConn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	sockets := make([]*websocket.Conn, 0, len(c.sockets))
	for ws := range c.sockets {
		sockets = append(sockets, ws)
	}
	c.mu.Unlock()
	_ = c.ws.Close()
	for _, ws := range sockets {
		_ = ws.Close()
	}
}

func proxyWebSockets(a, b *websocket.Conn) {
	done := make(chan struct{}, 2)
	copyOne := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			kind, data, err := src.ReadMessage()
			if err != nil {
				return
			}
			if err := dst.WriteMessage(kind, data); err != nil {
				return
			}
		}
	}
	go copyOne(a, b)
	go copyOne(b, a)
	<-done
}

func validToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func secureEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; object-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func Serve(ctx context.Context, addr string, handler http.Handler) error {
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       75 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}
