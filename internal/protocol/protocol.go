package protocol

const (
	DataInputOutput  byte = 0
	DataResize       byte = 1
	DataFileUpload   byte = 2
	DataUploadResult byte = 3
)

type ControlMessage struct {
	Type          string `json:"type"`
	SessionID     string `json:"session_id,omitempty"`
	Key           string `json:"key,omitempty"`
	RotateSeconds int64  `json:"rotate_seconds,omitempty"`
	RotateVersion int64  `json:"rotate_version,omitempty"`
	Note          string `json:"note,omitempty"`
	NoteVersion   int64  `json:"note_version,omitempty"`
}

// UploadHeader precedes the raw file bytes inside a DataFileUpload frame.
type UploadHeader struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// UploadResult is returned to the browser in a DataUploadResult frame.
type UploadResult struct {
	OK      bool   `json:"ok"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Message string `json:"message,omitempty"`
}

type Resize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}
