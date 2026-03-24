package ipc

import (
	"encoding/json"
)

// MessageType identifies the type of IPC message
type MessageType string

const (
	// TypeMatrixEvent is a Matrix event forwarded from AS to stdio mode
	TypeMatrixEvent MessageType = "matrix_event"
	// TypeReply is a reply from Claude Code to be sent to Matrix
	TypeReply MessageType = "reply"
)

// Message is the IPC message envelope
type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// MatrixEventPayload contains the Matrix event data
type MatrixEventPayload struct {
	RoomID    string `json:"room_id"`
	EventID   string `json:"event_id"`
	Sender    string `json:"sender"`
	Content   string `json:"content"`
	ThreadID  string `json:"thread_id,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// ReplyPayload contains the reply data to send to Matrix
type ReplyPayload struct {
	RoomID   string `json:"room_id"`
	ThreadID string `json:"thread_id,omitempty"`
	Content  string `json:"content"`
	Format   string `json:"format,omitempty"`
}
