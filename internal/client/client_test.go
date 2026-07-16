package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestNotify(t *testing.T) {
	t.Parallel()
	requests := make(chan map[string]string, 1)
	notifyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type: %q", r.Header.Get("Content-Type"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		requests <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer notifyServer.Close()

	c, err := New(Config{
		ServerURL:   "http://127.0.0.1:8080",
		RotateEvery: time.Hour,
		NotifyURL:   notifyServer.URL,
		NotifyUser:  "xiuxiu10201",
	})
	if err != nil {
		t.Fatal(err)
	}
	const link = "http://1.2.3.4:8080/s/token/"
	if err := c.notify(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	body := <-requests
	if body["user"] != "xiuxiu10201" || body["msg"] != link {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	_, err := New(Config{ServerURL: "127.0.0.1:8080", RotateEvery: time.Hour, NotifyURL: "x", NotifyUser: "x"})
	if err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestNotifyParametersRequired(t *testing.T) {
	t.Parallel()
	_, err := New(Config{
		ServerURL:   "http://127.0.0.1:8080",
		RotateEvery: time.Hour,
	})
	if err == nil {
		t.Fatal("expected missing notification parameters to be rejected")
	}
}

func TestNotifyLive(t *testing.T) {
	if os.Getenv("ANYSSH_LIVE_NOTIFY") != "1" {
		t.Skip("set ANYSSH_LIVE_NOTIFY=1 to send a real notification")
	}
	c, err := New(Config{
		ServerURL:   "http://127.0.0.1:8080",
		RotateEvery: time.Hour,
		NotifyURL:   "https://notify.bidd.net",
		NotifyUser:  "xiuxiu10201",
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := fmt.Sprintf("AnySSH notification test at %s", time.Now().UTC().Format(time.RFC3339))
	if err := c.notify(context.Background(), msg); err != nil {
		t.Fatalf("send live notification: %v", err)
	}
	t.Logf("notification accepted: user=%s msg=%q", c.cfg.NotifyUser, msg)
}

func TestLoginShellCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		shell string
		args  []string
	}{
		{shell: "/bin/bash", args: []string{"/bin/bash", "--login", "-i"}},
		{shell: "/bin/zsh", args: []string{"/bin/zsh", "-l", "-i"}},
		{shell: "/usr/bin/fish", args: []string{"/usr/bin/fish", "--login", "--interactive"}},
		{shell: "/bin/sh", args: []string{"/bin/sh", "-l"}},
	}
	for _, test := range tests {
		cmd := loginShellCommand(context.Background(), test.shell)
		if fmt.Sprint(cmd.Args) != fmt.Sprint(test.args) {
			t.Errorf("shell %s: got %v, want %v", test.shell, cmd.Args, test.args)
		}
	}
}
