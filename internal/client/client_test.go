package client

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	_, err := New(Config{ServerURL: "127.0.0.1:8080", RotateEvery: time.Hour})
	if err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestDeviceInfo(t *testing.T) {
	t.Parallel()
	first := makeDeviceInfo("host name\n", "deploy user", "linux", "arm64", []byte("machine-id"))
	second := makeDeviceInfo("other", "other", "linux", "amd64", []byte("machine-id"))
	if first.Hostname != "host_name" || first.Username != "deploy_user" {
		t.Fatalf("device fields were not sanitized: %+v", first)
	}
	if first.ID != second.ID || len(first.ID) != 16 {
		t.Fatalf("device ID is not stable: %q %q", first.ID, second.ID)
	}
}

func TestLoginShellCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		shell string
		args  []string
	}{
		{"/bin/bash", []string{"/bin/bash", "--login", "-i"}},
		{"/bin/zsh", []string{"/bin/zsh", "-l", "-i"}},
		{"/usr/bin/fish", []string{"/usr/bin/fish", "--login", "--interactive"}},
		{"/bin/sh", []string{"/bin/sh", "-l"}},
	}
	for _, test := range tests {
		cmd := loginShellCommand(context.Background(), test.shell)
		if fmt.Sprint(cmd.Args) != fmt.Sprint(test.args) {
			t.Errorf("shell %s: got %v, want %v", test.shell, cmd.Args, test.args)
		}
	}
}
