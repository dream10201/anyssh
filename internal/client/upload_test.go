package client

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"anyssh/internal/protocol"
)

func buildUploadFrame(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	header, err := json.Marshal(protocol.UploadHeader{Name: name, Size: int64(len(content))})
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4+len(header)+len(content))
	binary.BigEndian.PutUint32(body[:4], uint32(len(header)))
	copy(body[4:], header)
	copy(body[4+len(header):], content)
	return body
}

func TestHandleUploadWritesFile(t *testing.T) {
	content := []byte("hello world")
	result := handleUpload(buildUploadFrame(t, "anyssh-upload-test.txt", content), 0)
	if !result.OK {
		t.Fatalf("upload failed: %s", result.Message)
	}
	t.Cleanup(func() { _ = os.Remove(result.Path) })
	got, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: %q", got)
	}
	if filepath.Base(result.Path) != "anyssh-upload-test.txt" {
		t.Fatalf("unexpected path: %s", result.Path)
	}
}

func TestSanitizeUploadNameRejectsTraversal(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd": "passwd",
		"/abs/name.sh":     "name.sh",
		"plain.txt":        "plain.txt",
		"..":               "",
		"":                 "",
	}
	for in, want := range cases {
		if got := sanitizeUploadName(in); got != want {
			t.Errorf("sanitizeUploadName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUploadRejectsShortFrame(t *testing.T) {
	if _, _, err := parseUpload([]byte{0, 1}); err == nil {
		t.Fatal("expected error for short frame")
	}
}

func TestNoteStorePersistsAndReconciles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".anyssh-note.json")
	store := &noteStore{path: path}

	if !store.apply("first", 1) {
		t.Fatal("expected first apply to persist")
	}
	if store.apply("stale", 0) {
		t.Fatal("older version must not overwrite")
	}
	text, version := store.snapshot()
	if text != "first" || version != 1 {
		t.Fatalf("snapshot mismatch: %q v%d", text, version)
	}

	reloaded := &noteStore{path: path}
	reloaded.load()
	if got, v := reloaded.snapshot(); got != "first" || v != 1 {
		t.Fatalf("reloaded mismatch: %q v%d", got, v)
	}
}

func TestSanitizeNoteStripsControlChars(t *testing.T) {
	if got := sanitizeNote("ok\x00\x07line\nnext"); got != "okline\nnext" {
		t.Fatalf("sanitizeNote = %q", got)
	}
}
