package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"anyssh/internal/protocol"
)

type adminClient struct {
	ID            string     `json:"id"`
	Hostname      string     `json:"hostname"`
	Username      string     `json:"username"`
	OS            string     `json:"os"`
	Arch          string     `json:"arch"`
	Link          string     `json:"link"`
	RegisteredAt  time.Time  `json:"registered_at"`
	Disabled      bool       `json:"disabled"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RotateSeconds int64      `json:"rotate_seconds"`
	Note          string     `json:"note"`
}

const maxNoteLength = 1024

// decodeNoteHeader decodes the base64 note a client reports on registration and
// normalizes it. Invalid encodings are treated as an empty note.
func decodeNoteHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return ""
	}
	return cleanNote(string(raw))
}

// cleanNote strips control characters (keeping tab and newline) and caps the
// length so a note is safe to store and forward.
func cleanNote(note string) string {
	note = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, note)
	if len(note) > maxNoteLength {
		note = note[:maxNoteLength]
	}
	return note
}

func cleanHeader(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > 128 {
		value = value[:128]
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return '_'
		}
		return r
	}, value)
}

func (c *clientConn) available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.disabled && (c.expiresAt.IsZero() || time.Now().Before(c.expiresAt))
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin")
	if path == "" {
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
		return
	}
	r.URL.Path = path
	s.admin.ServeHTTP(w, r)
}

func (s *Server) handleAdminClients(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	s.mu.Lock()
	clients := make([]*clientConn, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	result := make([]adminClient, 0, len(clients))
	for _, c := range clients {
		c.mu.Lock()
		item := adminClient{ID: c.deviceID, Hostname: c.hostname, Username: c.username, OS: c.osName, Arch: c.arch, Link: c.link, RegisteredAt: c.registeredAt, Disabled: c.disabled, RotateSeconds: c.rotateSeconds, Note: c.note}
		if !c.expiresAt.IsZero() {
			x := c.expiresAt
			item.ExpiresAt = &x
		}
		c.mu.Unlock()
		result = append(result, item)
	}
	// Stable ordering keeps the admin list from reshuffling between refreshes and
	// lets the browser skip re-rendering when nothing has actually changed.
	sort.Slice(result, func(i, j int) bool {
		if result[i].Hostname != result[j].Hostname {
			return result[i].Hostname < result[j].Hostname
		}
		return result[i].ID < result[j].ID
	})
	writeJSON(w, result)
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"public_url": s.publicURL,
			"direct_url": requestServerURL(r),
		})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleAdminClientAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/admin/clients/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	c := s.clientByDevice(parts[0])
	if c == nil {
		http.Error(w, "client not found", 404)
		return
	}
	switch parts[1] {
	case "rotation":
		var body struct {
			RotateSeconds int64 `json:"rotate_seconds"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.RotateSeconds < 0 {
			http.Error(w, "invalid rotation interval", 400)
			return
		}
		c.mu.Lock()
		version := time.Now().UnixNano()
		if version <= c.rotateVersion {
			version = c.rotateVersion + 1
		}
		c.rotateSeconds = body.RotateSeconds
		c.rotateVersion = version
		c.mu.Unlock()
		s.mu.Lock()
		s.rotations[c.deviceID] = rotationSetting{seconds: body.RotateSeconds, version: version}
		s.mu.Unlock()
		if err := c.writeJSON(protocol.ControlMessage{Type: "set_rotate", RotateSeconds: body.RotateSeconds, RotateVersion: version}); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
	case "note":
		var body struct {
			Note string `json:"note"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		note := cleanNote(body.Note)
		c.mu.Lock()
		version := time.Now().UnixNano()
		if version <= c.noteVersion {
			version = c.noteVersion + 1
		}
		c.note = note
		c.noteVersion = version
		c.mu.Unlock()
		s.mu.Lock()
		s.notes[c.deviceID] = noteSetting{text: note, version: version}
		s.mu.Unlock()
		if err := c.writeJSON(protocol.ControlMessage{Type: "set_note", Note: note, NoteVersion: version}); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
	case "disable":
		var body struct {
			Disabled bool `json:"disabled"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		c.mu.Lock()
		c.disabled = body.Disabled
		c.mu.Unlock()
		if body.Disabled {
			c.closeSessions()
		}
	case "expire":
		var body struct {
			ExpiresAt string `json:"expires_at"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		var expiry time.Time
		var err error
		if body.ExpiresAt != "" {
			expiry, err = time.Parse(time.RFC3339, body.ExpiresAt)
		}
		if err != nil {
			http.Error(w, "invalid expiry", 400)
			return
		}
		c.mu.Lock()
		c.expiresAt = expiry
		c.mu.Unlock()
		if !expiry.IsZero() {
			go func(want time.Time) {
				if delay := time.Until(want); delay > 0 {
					timer := time.NewTimer(delay)
					<-timer.C
				}
				c.mu.Lock()
				active := c.expiresAt.Equal(want)
				c.mu.Unlock()
				if active {
					c.closeSessions()
				}
			}(expiry)
		}
	case "rotate":
		if err := c.writeJSON(protocol.ControlMessage{Type: "rotate"}); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) clientByDevice(id string) *clientConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.clients {
		if c.deviceID == id {
			return c
		}
	}
	return nil
}

func (s *Server) notifyClientLink(c *clientConn) {
	if s.weComKey == "" {
		return
	}
	content := fmt.Sprintf("AnySSH new link\nDevice: %s\nUser: %s\nSystem: %s/%s\nID: %s\nOpen terminal: %s", c.hostname, c.username, c.osName, c.arch, c.deviceID, c.link)
	if err := s.postWeCom(content); err != nil {
		s.logger.Warn("enterprise WeChat notification failed", "error", err)
	}
}

func (s *Server) postWeCom(content string) error {
	if s.weComKey == "" {
		return errors.New("ANYSSH_WECOM_KEY is not configured")
	}
	hook := s.weComEndpoint + "?key=" + url.QueryEscape(s.weComKey)
	body, _ := json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": content}})
	resp, err := http.Post(hook, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(data, &result) != nil || result.ErrCode != 0 {
		return fmt.Errorf("WeCom error: %s", result.ErrMsg)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (c *clientConn) closeSessions() {
	c.mu.Lock()
	sockets := make([]io.Closer, 0, len(c.sockets))
	for ws := range c.sockets {
		sockets = append(sockets, ws)
	}
	c.mu.Unlock()
	for _, ws := range sockets {
		_ = ws.Close()
	}
}
