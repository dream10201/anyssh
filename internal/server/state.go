package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type savedSettings struct {
	WebhookURL string `json:"wecom_webhook_url"`
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
	if settings.WebhookURL != "" && validateWebhook(settings.WebhookURL) != nil {
		return errors.New("state file contains an invalid enterprise WeChat webhook")
	}
	s.webhookURL = settings.WebhookURL
	return nil
}

func (s *Server) saveSettings() error {
	if s.dataFile == "" {
		return nil
	}
	s.mu.Lock()
	settings := savedSettings{WebhookURL: s.webhookURL}
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
