package bootstrap

import (
	"bytes"
	"testing"
)

func TestAppendAndParse(t *testing.T) {
	t.Parallel()
	base := []byte("ELF binary data")
	want := Config{ServerURL: "https://ssh.example.com", Rotate: "30m", Secret: "secret"}
	data, err := Append(base, want)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != want {
		t.Fatalf("got found=%v config=%+v, want %+v", found, got, want)
	}
}

func TestParseRejectsTamperedPayload(t *testing.T) {
	t.Parallel()
	data, err := Append([]byte("binary"), Config{ServerURL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	data[len("binary")] ^= 1
	_, found, err := Parse(bytes.NewReader(data), int64(len(data)))
	if !found || err == nil {
		t.Fatalf("expected checksum error, found=%v err=%v", found, err)
	}
}

func TestParseWithoutTrailer(t *testing.T) {
	t.Parallel()
	_, found, err := Parse(bytes.NewReader([]byte("plain binary")), int64(len("plain binary")))
	if err != nil || found {
		t.Fatalf("unexpected result: found=%v err=%v", found, err)
	}
}
