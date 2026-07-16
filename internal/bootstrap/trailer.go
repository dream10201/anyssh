package bootstrap

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	magic         = "ANYSSH_CONFIG_V1"
	footerSize    = sha256.Size + 8 + len(magic)
	maxConfigSize = 1024 * 1024
)

type Config struct {
	ServerURL string `json:"server_url"`
	Rotate    string `json:"rotate,omitempty"`
	Secret    string `json:"secret,omitempty"`
}

func Append(binaryData []byte, cfg Config) ([]byte, error) {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxConfigSize {
		return nil, errors.New("embedded configuration is too large")
	}
	sum := sha256.Sum256(payload)
	result := make([]byte, 0, len(binaryData)+len(payload)+footerSize)
	result = append(result, binaryData...)
	result = append(result, payload...)
	result = append(result, sum[:]...)
	length := make([]byte, 8)
	binary.BigEndian.PutUint64(length, uint64(len(payload)))
	result = append(result, length...)
	result = append(result, magic...)
	return result, nil
}

func ReadExecutable() (Config, bool, error) {
	path, err := os.Executable()
	if err != nil {
		return Config{}, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Config{}, false, err
	}
	return Parse(file, info.Size())
}

func Parse(reader io.ReaderAt, size int64) (Config, bool, error) {
	if size < int64(footerSize) {
		return Config{}, false, nil
	}
	footer := make([]byte, footerSize)
	if _, err := reader.ReadAt(footer, size-int64(footerSize)); err != nil {
		return Config{}, false, err
	}
	if string(footer[sha256.Size+8:]) != magic {
		return Config{}, false, nil
	}
	payloadLength := binary.BigEndian.Uint64(footer[sha256.Size : sha256.Size+8])
	if payloadLength == 0 || payloadLength > maxConfigSize || payloadLength > uint64(size-int64(footerSize)) {
		return Config{}, true, errors.New("invalid embedded configuration length")
	}
	payload := make([]byte, payloadLength)
	if _, err := reader.ReadAt(payload, size-int64(footerSize)-int64(payloadLength)); err != nil {
		return Config{}, true, err
	}
	sum := sha256.Sum256(payload)
	if subtle.ConstantTimeCompare(sum[:], footer[:sha256.Size]) != 1 {
		return Config{}, true, errors.New("embedded configuration checksum mismatch")
	}
	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return Config{}, true, fmt.Errorf("decode embedded configuration: %w", err)
	}
	return cfg, true, nil
}
