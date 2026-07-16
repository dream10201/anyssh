package protocol

const (
	DataInputOutput byte = 0
	DataResize      byte = 1
)

type ControlMessage struct {
	Type          string `json:"type"`
	SessionID     string `json:"session_id,omitempty"`
	Key           string `json:"key,omitempty"`
	RotateSeconds int64  `json:"rotate_seconds,omitempty"`
}

type Resize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}
