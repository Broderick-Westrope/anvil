package proto

// Session represents a session in the proto layer.
type Session struct {
	ID               string  `json:"id"`
	ParentSessionID  string  `json:"parent_session_id"`
	Title            string  `json:"title"`
	MessageCount     int64   `json:"message_count"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	LeafMessageID    string  `json:"leaf_message_id"`
	Cost             float64 `json:"cost"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
	Pinned           bool    `json:"pinned"`
	PinNote          string  `json:"pin_note"`
}

// SetSessionPinRequest is the request body for pinning or unpinning a
// session.
type SetSessionPinRequest struct {
	Pinned bool   `json:"pinned"`
	Note   string `json:"note"`
}
