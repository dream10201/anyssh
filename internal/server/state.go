package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type savedSettings struct {
	Secret        string `json:"client_secret"`
	RotateSeconds *int64 `json:"rotate_seconds,omitempty"`
}

func (s *Server) loadSettings() error {
	if s.dataFile == "" {
		return nil
	}
	data, err := os.ReadFile(s.dataFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state file: %w", err)
	}
	var settings savedSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("decode state file: %w", err)
	}
	if s.secret == "" {
		s.secret = settings.Secret
	}
	if settings.RotateSeconds != nil && *settings.RotateSeconds >= 0 {
		s.clientRotate = time.Duration(*settings.RotateSeconds) * time.Second
	}
	return nil
}

func (s *Server) saveSettings() error {
	if s.dataFile == "" {
		return nil
	}
	s.mu.Lock()
	seconds := int64(s.clientRotate / time.Second)
	settings := savedSettings{Secret: s.secret, RotateSeconds: &seconds}
	s.mu.Unlock()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.dataFile), 0700); err != nil {
		return err
	}
	tmp := s.dataFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.dataFile)
}
