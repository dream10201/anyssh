package client

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strings"
	"unicode"
)

type deviceInfo struct {
	Hostname string
	Username string
	OS       string
	Arch     string
	ID       string
}

func detectDeviceInfo() deviceInfo {
	hostname, _ := os.Hostname()
	username := "unknown"
	if current, err := user.Current(); err == nil {
		username = current.Username
	}
	seed := machineID()
	if len(seed) == 0 {
		seed = []byte(hostname + "\x00" + username + "\x00" + runtime.GOOS + "\x00" + runtime.GOARCH)
	}
	return makeDeviceInfo(hostname, username, runtime.GOOS, runtime.GOARCH, seed)
}

func makeDeviceInfo(hostname, username, goos, arch string, seed []byte) deviceInfo {
	sum := sha256.Sum256(seed)
	return deviceInfo{
		Hostname: cleanDeviceField(hostname),
		Username: cleanDeviceField(username),
		OS:       cleanDeviceField(goos),
		Arch:     cleanDeviceField(arch),
		ID:       hex.EncodeToString(sum[:8]),
	}
}

func machineID() []byte {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		data, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(data)) != "" {
			return []byte(strings.TrimSpace(string(data)))
		}
	}
	return nil
}

func cleanDeviceField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return '_'
		}
		return r
	}, value)
	runes := []rune(cleaned)
	if len(runes) > 64 {
		cleaned = string(runes[:64])
	}
	return cleaned
}

func (d deviceInfo) notificationMessage(link string) string {
	return fmt.Sprintf("AnySSH device=%s user=%s os=%s/%s id=%s url=%s", d.Hostname, d.Username, d.OS, d.Arch, d.ID, link)
}
