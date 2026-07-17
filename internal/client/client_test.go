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

func TestPermanentRotationIsAccepted(t *testing.T) {
	t.Parallel()
	c, err := New(Config{ServerURL: "http://127.0.0.1:8080", RotateEvery: 0})
	if err != nil {
		t.Fatal(err)
	}
	if c.rotation.Load() != 0 {
		t.Fatalf("rotation=%d, want permanent", c.rotation.Load())
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

func TestFirstAvailableShell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		available  map[string]bool
		candidates []string
		want       string
	}{
		{name: "prefer first", available: map[string]bool{"/bin/bash": true, "/bin/sh": true}, candidates: []string{"/bin/bash", "/bin/sh"}, want: "/bin/bash"},
		{name: "fall back to next", available: map[string]bool{"/bin/sh": true}, candidates: []string{"/bin/bash", "/bin/sh"}, want: "/bin/sh"},
		{name: "reject relative path", available: map[string]bool{"custom-shell": true, "/opt/shell": true}, candidates: []string{"custom-shell", "/opt/shell"}, want: "/opt/shell"},
		{name: "no shell", available: map[string]bool{}, candidates: []string{"/bin/bash", "/bin/sh"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shell := firstAvailableShell(func(path string) bool { return test.available[path] }, test.candidates...)
			if shell != test.want {
				t.Fatalf("shell=%q, want %q", shell, test.want)
			}
		})
	}
}

func TestFindStandardShell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		available map[string]bool
		paths     map[string]string
		want      string
	}{
		{
			name:      "find bash outside bin before sh",
			available: map[string]bool{"/nix/store/current/bin/bash": true, "/bin/sh": true},
			paths:     map[string]string{"bash": "/nix/store/current/bin/bash", "sh": "/bin/sh"},
			want:      "/nix/store/current/bin/bash",
		},
		{
			name:      "use common usr path without PATH",
			available: map[string]bool{"/usr/bin/bash": true},
			paths:     map[string]string{},
			want:      "/usr/bin/bash",
		},
		{
			name:      "fall back to sh from PATH",
			available: map[string]bool{"/opt/tools/sh": true},
			paths:     map[string]string{"sh": "/opt/tools/sh"},
			want:      "/opt/tools/sh",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shell := findStandardShell(
				func(path string) bool { return test.available[path] },
				func(name string) (string, error) {
					path, ok := test.paths[name]
					if !ok {
						return "", fmt.Errorf("%s not found", name)
					}
					return path, nil
				},
			)
			if shell != test.want {
				t.Fatalf("shell=%q, want %q", shell, test.want)
			}
		})
	}
}
