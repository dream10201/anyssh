package protocol

const (
	DataInputOutput byte = 0
	DataResize      byte = 1
)

type ControlMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Key       string `json:"key,omitempty"`
}

type Resize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}
