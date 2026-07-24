package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxNoteLength = 1024

// noteStore persists the admin-supplied device note beside the client binary so
// it survives client restarts and can be reported back to the server on
// reconnect. The server treats the highest version as authoritative.
type noteStore struct {
	mu      sync.Mutex
	path    string
	text    string
	version int64
}

type noteFile struct {
	Note    string `json:"note"`
	Version int64  `json:"version"`
}

func newNoteStore() *noteStore {
	s := &noteStore{path: notePath()}
	s.load()
	return s
}

func notePath() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), ".anyssh-note.json")
	}
	return ".anyssh-note.json"
}

func (s *noteStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var nf noteFile
	if json.Unmarshal(data, &nf) != nil {
		return
	}
	s.text = sanitizeNote(nf.Note)
	if nf.Version > 0 {
		s.version = nf.Version
	}
}

func (s *noteStore) snapshot() (string, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text, s.version
}

// apply stores the note when the incoming version is at least the current one.
// It returns true when the on-disk state changed.
func (s *noteStore) apply(text string, version int64) bool {
	text = sanitizeNote(text)
	s.mu.Lock()
	defer s.mu.Unlock()
	if version < s.version {
		return false
	}
	if version == s.version && text == s.text {
		return false
	}
	s.text = text
	s.version = version
	return s.persist()
}

func (s *noteStore) persist() bool {
	data, err := json.Marshal(noteFile{Note: s.text, Version: s.version})
	if err != nil {
		return false
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return false
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return false
	}
	return true
}

func sanitizeNote(note string) string {
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
