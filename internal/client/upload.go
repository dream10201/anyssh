package client

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"anyssh/internal/protocol"
)

const maxUploadSize = 64 << 20 // 64 MiB per file

// handleUpload decodes a DataFileUpload frame body (a 4-byte big-endian header
// length, a JSON UploadHeader, then the raw file bytes) and writes the file
// into the shell's current working directory. shellPid identifies the live PTY
// shell so the file lands where the operator currently is.
func handleUpload(body []byte, shellPid int) protocol.UploadResult {
	name, content, err := parseUpload(body)
	if err != nil {
		return protocol.UploadResult{OK: false, Message: err.Error()}
	}
	dir := uploadDir(shellPid)
	dest := filepath.Join(dir, name)
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		return protocol.UploadResult{OK: false, Name: name, Message: fmt.Sprintf("写入失败: %v", err)}
	}
	return protocol.UploadResult{OK: true, Name: name, Path: dest, Size: int64(len(content))}
}

func parseUpload(body []byte) (name string, content []byte, err error) {
	if len(body) < 4 {
		return "", nil, errors.New("上传数据不完整")
	}
	headerLen := binary.BigEndian.Uint32(body[:4])
	if int(headerLen) > len(body)-4 {
		return "", nil, errors.New("上传头部长度非法")
	}
	var header protocol.UploadHeader
	if err := json.Unmarshal(body[4:4+headerLen], &header); err != nil {
		return "", nil, errors.New("上传头部解析失败")
	}
	content = body[4+headerLen:]
	if len(content) > maxUploadSize {
		return "", nil, fmt.Errorf("文件超过 %d MiB 限制", maxUploadSize>>20)
	}
	name = sanitizeUploadName(header.Name)
	if name == "" {
		return "", nil, errors.New("文件名非法")
	}
	return name, content, nil
}

// sanitizeUploadName reduces an arbitrary client-supplied name to a safe base
// file name so an upload can never escape the target directory.
func sanitizeUploadName(raw string) string {
	name := filepath.Base(filepath.FromSlash(raw))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// uploadDir returns the shell's current working directory when it can be read
// from /proc, falling back to the user's home directory and then the process
// working directory.
func uploadDir(shellPid int) string {
	if shellPid > 0 {
		if dir, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", shellPid)); err == nil {
			if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
				return dir
			}
		}
	}
	if current, err := user.Current(); err == nil && current.HomeDir != "" {
		return current.HomeDir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}
