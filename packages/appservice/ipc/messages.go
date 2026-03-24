package ipc

import (
	"encoding/json"
	"time"
)

// MessageType identifies the type of IPC message
type MessageType string

const (
	// Coordinator → Bridge messages
	TypeMatrixMessage      MessageType = "matrix_message"
	TypePermissionVerdict  MessageType = "permission_verdict"
	TypeShutdown           MessageType = "shutdown"
	TypePing               MessageType = "ping"

	// Bridge → Coordinator messages
	TypeReply              MessageType = "reply"
	TypeStreamingReply     MessageType = "streaming_reply"
	TypePermissionRequest  MessageType = "permission_request"
	TypeStatus             MessageType = "status"
	TypePong               MessageType = "pong"
)

// IPCMessage is the envelope for all IPC messages
type IPCMessage struct {
	Type      MessageType     `json:"type"`
	SessionID string          `json:"session_id"`
	Timestamp int64           `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// NewIPCMessage creates a new IPC message with the given type and payload
func NewIPCMessage(msgType MessageType, sessionID string, payload interface{}) (*IPCMessage, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &IPCMessage{
		Type:      msgType,
		SessionID: sessionID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   data,
	}, nil
}

// ParsePayload unmarshals the payload into the given target
func (m *IPCMessage) ParsePayload(target interface{}) error {
	return json.Unmarshal(m.Payload, target)
}

// --- Coordinator → Bridge payloads ---

// MatrixMessagePayload contains a Matrix message to forward to Claude
type MatrixMessagePayload struct {
	RoomID    string `json:"room_id"`
	ThreadID  string `json:"thread_id,omitempty"`
	Sender    string `json:"sender"`
	EventID   string `json:"event_id"`
	Body      string `json:"body"`
	Timestamp int64  `json:"timestamp"`
}

// PermissionVerdictPayload contains the user's permission decision
type PermissionVerdictPayload struct {
	RequestID string `json:"request_id"`
	Allowed   bool   `json:"allowed"`
}

// ShutdownPayload contains shutdown instructions
type ShutdownPayload struct {
	Reason  string `json:"reason"`
	Timeout int    `json:"timeout_seconds"`
}

// PingPayload is empty, used for health checks
type PingPayload struct{}

// --- Bridge → Coordinator payloads ---

// ReplyPayload contains Claude's reply to send to Matrix
type ReplyPayload struct {
	RoomID   string `json:"room_id"`
	ThreadID string `json:"thread_id,omitempty"`
	Text     string `json:"text"`
}

// StreamingReplyPayload contains a streaming reply chunk
type StreamingReplyPayload struct {
	RoomID   string `json:"room_id"`
	ThreadID string `json:"thread_id,omitempty"`
	Chunk    string `json:"chunk"`
	IsFinal  bool   `json:"is_final"`
}

// PermissionRequestPayload contains a permission request from Claude Code
type PermissionRequestPayload struct {
	RequestID    string `json:"request_id"`
	ToolName     string `json:"tool_name"`
	Description  string `json:"description"`
	InputPreview string `json:"input_preview"`
	RoomID       string `json:"room_id"`
}

// StatusPayload contains bridge status information
type StatusPayload struct {
	Status    string `json:"status"` // "ready", "busy", "error", "shutdown"
	Error     string `json:"error,omitempty"`
	ClaudePID int    `json:"claude_pid,omitempty"`
}

// PongPayload is the response to a ping
type PongPayload struct {
	Uptime int64 `json:"uptime_ms"`
}

// SessionInfo contains information about an active session
type SessionInfo struct {
	SessionID   string    `json:"session_id"`
	RoomID      string    `json:"room_id"`
	ThreadID    string    `json:"thread_id,omitempty"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	LastActive  time.Time `json:"last_active"`
	MessageCount int      `json:"message_count"`
}
