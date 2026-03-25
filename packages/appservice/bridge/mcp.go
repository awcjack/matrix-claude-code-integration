// Package bridge implements the child MCP server that runs with each Claude Code instance
package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// MCPServer implements the MCP protocol for a single Claude Code session
type MCPServer struct {
	name         string
	version      string
	instructions string
	sessionID    string
	roomID       string
	threadID     string

	// Callbacks
	replyHandler          func(roomID, threadID, text string) error
	streamingReplyHandler func(roomID, threadID, chunk string, isFinal bool) error
	permissionHandler     func(requestID, toolName, description, inputPreview string) error

	// Pending permission requests
	permMu      sync.Mutex
	pendingPerm map[string]chan bool // requestID -> result channel

	// IO
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex
}

// MCPRequest represents an incoming MCP request
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// MCPResponse represents an outgoing MCP response
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
}

// MCPNotification represents an outgoing MCP notification
type MCPNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// MCPError represents an MCP error
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewMCPServer creates a new MCP server for a bridge session
func NewMCPServer(sessionID, roomID, threadID string) *MCPServer {
	return &MCPServer{
		name:      "matrix-bridge",
		version:   "1.0.0",
		sessionID: sessionID,
		roomID:    roomID,
		threadID:  threadID,
		instructions: `Messages from Matrix arrive as <channel source="matrix" room_id="..." sender="..." thread_id="...">.
Reply to Matrix messages using the 'reply' tool, passing the room_id and optionally thread_id from the original message.
Each Matrix thread maintains context - replies in threads should reference the thread_id.`,
		pendingPerm: make(map[string]chan bool),
		reader:      bufio.NewReader(os.Stdin),
		writer:      os.Stdout,
	}
}

// SetReplyHandler sets the callback for reply tool invocations
func (s *MCPServer) SetReplyHandler(handler func(roomID, threadID, text string) error) {
	s.replyHandler = handler
}

// SetStreamingReplyHandler sets the callback for streaming reply tool invocations
func (s *MCPServer) SetStreamingReplyHandler(handler func(roomID, threadID, chunk string, isFinal bool) error) {
	s.streamingReplyHandler = handler
}

// SetPermissionHandler sets the callback for permission requests
func (s *MCPServer) SetPermissionHandler(handler func(requestID, toolName, description, inputPreview string) error) {
	s.permissionHandler = handler
}

// Run starts the MCP server
func (s *MCPServer) Run(ctx context.Context) error {
	log.Printf("===== Bridge MCP server Run() starting for session %s =====", s.sessionID)
	log.Printf("MCP server name: %s, version: %s", s.name, s.version)
	log.Printf("Reading from stdin, writing to stdout")

	for {
		select {
		case <-ctx.Done():
			log.Printf("MCP server context cancelled")
			return ctx.Err()
		default:
		}

		log.Printf("Waiting for input from Claude...")
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				log.Printf("MCP server received EOF on stdin")
				return nil
			}
			log.Printf("MCP server read error: %v", err)
			return fmt.Errorf("read error: %w", err)
		}

		log.Printf("MCP received line: %s", line)

		if line == "" || line == "\n" {
			continue
		}

		var req MCPRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			log.Printf("Invalid JSON request: %v, line: %s", err, line)
			continue
		}

		log.Printf("MCP handling request: method=%s id=%v", req.Method, req.ID)
		s.handleRequest(ctx, &req)
	}
}

// handleRequest processes an incoming MCP request
func (s *MCPServer) handleRequest(ctx context.Context, req *MCPRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "initialized":
		log.Printf("Client initialized")
	case "tools/list":
		s.handleListTools(req)
	case "tools/call":
		s.handleCallTool(ctx, req)
	case "ping":
		s.sendResponse(req.ID, map[string]interface{}{})
	case "notifications/claude/channel/permission_request":
		s.handlePermissionRequest(ctx, req)
	default:
		log.Printf("Unknown method: %s", req.Method)
	}
}

// handleInitialize responds to the initialize request
func (s *MCPServer) handleInitialize(req *MCPRequest) {
	result := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"serverInfo": map[string]interface{}{
			"name":    s.name,
			"version": s.version,
		},
		"capabilities": map[string]interface{}{
			"experimental": map[string]interface{}{
				"claude/channel":            map[string]interface{}{},
				"claude/channel/permission": map[string]interface{}{},
			},
			"tools": map[string]interface{}{},
		},
		"instructions": s.instructions,
	}

	s.sendResponse(req.ID, result)
}

// handleListTools responds with available tools
func (s *MCPServer) handleListTools(req *MCPRequest) {
	tools := []map[string]interface{}{
		{
			"name":        "reply",
			"description": "Send a reply back to the Matrix room/thread",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"room_id": map[string]interface{}{
						"type":        "string",
						"description": "The Matrix room ID to send the reply to",
					},
					"thread_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional thread ID to reply in a specific thread",
					},
					"text": map[string]interface{}{
						"type":        "string",
						"description": "The message text to send",
					},
				},
				"required": []string{"room_id", "text"},
			},
		},
		{
			"name":        "streaming_reply",
			"description": "Send a streaming reply to Matrix, allowing incremental updates as the response is generated",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"room_id": map[string]interface{}{
						"type":        "string",
						"description": "The Matrix room ID to send the reply to",
					},
					"thread_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional thread ID to reply in a specific thread",
					},
					"chunk": map[string]interface{}{
						"type":        "string",
						"description": "The text chunk to append to the streaming message",
					},
					"is_final": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether this is the final chunk of the streaming message",
					},
				},
				"required": []string{"room_id", "chunk", "is_final"},
			},
		},
	}

	s.sendResponse(req.ID, map[string]interface{}{
		"tools": tools,
	})
}

// handleCallTool handles tool invocation
func (s *MCPServer) handleCallTool(ctx context.Context, req *MCPRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}

	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendError(req.ID, -32602, "Invalid params")
		return
	}

	switch params.Name {
	case "reply":
		s.handleReplyTool(ctx, req, params.Arguments)
	case "streaming_reply":
		s.handleStreamingReplyTool(ctx, req, params.Arguments)
	default:
		s.sendError(req.ID, -32601, fmt.Sprintf("Unknown tool: %s", params.Name))
	}
}

// handleReplyTool handles the reply tool invocation
func (s *MCPServer) handleReplyTool(ctx context.Context, req *MCPRequest, args json.RawMessage) {
	var replyArgs struct {
		RoomID   string `json:"room_id"`
		ThreadID string `json:"thread_id"`
		Text     string `json:"text"`
	}

	if err := json.Unmarshal(args, &replyArgs); err != nil {
		s.sendError(req.ID, -32602, "Invalid reply arguments")
		return
	}

	if replyArgs.RoomID == "" || replyArgs.Text == "" {
		s.sendError(req.ID, -32602, "room_id and text are required")
		return
	}

	// Validate room_id matches session context to prevent cross-room messaging
	if replyArgs.RoomID != s.roomID {
		log.Printf("Reply blocked: room_id %s does not match session room %s", replyArgs.RoomID, s.roomID)
		s.sendError(req.ID, -32602, "room_id must match the session's room")
		return
	}

	// Call the reply handler
	if s.replyHandler == nil {
		s.sendError(req.ID, -32603, "reply handler not configured")
		return
	}

	if err := s.replyHandler(replyArgs.RoomID, replyArgs.ThreadID, replyArgs.Text); err != nil {
		log.Printf("Reply handler error: %v", err)
		s.sendResponse(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("failed: %v", err)},
			},
		})
		return
	}

	s.sendResponse(req.ID, map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": "sent"},
		},
	})
}

// handleStreamingReplyTool handles the streaming_reply tool invocation
func (s *MCPServer) handleStreamingReplyTool(ctx context.Context, req *MCPRequest, args json.RawMessage) {
	var streamArgs struct {
		RoomID   string `json:"room_id"`
		ThreadID string `json:"thread_id"`
		Chunk    string `json:"chunk"`
		IsFinal  bool   `json:"is_final"`
	}

	if err := json.Unmarshal(args, &streamArgs); err != nil {
		s.sendError(req.ID, -32602, "Invalid streaming_reply arguments")
		return
	}

	if streamArgs.RoomID == "" || streamArgs.Chunk == "" {
		s.sendError(req.ID, -32602, "room_id and chunk are required")
		return
	}

	// Validate room_id matches session context to prevent cross-room messaging
	if streamArgs.RoomID != s.roomID {
		log.Printf("Streaming reply blocked: room_id %s does not match session room %s", streamArgs.RoomID, s.roomID)
		s.sendError(req.ID, -32602, "room_id must match the session's room")
		return
	}

	// Call the streaming reply handler
	if s.streamingReplyHandler == nil {
		s.sendError(req.ID, -32603, "streaming reply handler not configured")
		return
	}

	if err := s.streamingReplyHandler(streamArgs.RoomID, streamArgs.ThreadID, streamArgs.Chunk, streamArgs.IsFinal); err != nil {
		log.Printf("Streaming reply handler error: %v", err)
		s.sendResponse(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("failed: %v", err)},
			},
		})
		return
	}

	status := "chunk_sent"
	if streamArgs.IsFinal {
		status = "completed"
	}

	s.sendResponse(req.ID, map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": status},
		},
	})
}

// handlePermissionRequest processes permission requests from Claude Code
func (s *MCPServer) handlePermissionRequest(ctx context.Context, req *MCPRequest) {
	var permReq struct {
		RequestID    string `json:"request_id"`
		ToolName     string `json:"tool_name"`
		Description  string `json:"description"`
		InputPreview string `json:"input_preview"`
	}

	if err := json.Unmarshal(req.Params, &permReq); err != nil {
		log.Printf("Invalid permission request: %v", err)
		return
	}

	log.Printf("Permission request: id=%s tool=%s", permReq.RequestID, permReq.ToolName)

	// Forward to coordinator via handler
	if s.permissionHandler == nil {
		log.Printf("No permission handler set, denying request %s", permReq.RequestID)
		s.SendPermissionVerdict(permReq.RequestID, false)
		return
	}

	if err := s.permissionHandler(permReq.RequestID, permReq.ToolName, permReq.Description, permReq.InputPreview); err != nil {
		log.Printf("Permission handler error: %v", err)
		// Default deny on error
		s.SendPermissionVerdict(permReq.RequestID, false)
		return
	}

	// The verdict will be sent via SendPermissionVerdict when coordinator responds
}

// SendPermissionVerdict sends a permission verdict to Claude Code
func (s *MCPServer) SendPermissionVerdict(requestID string, allowed bool) {
	behavior := "deny"
	if allowed {
		behavior = "allow"
	}

	s.sendNotification("notifications/claude/channel/permission", map[string]interface{}{
		"request_id": requestID,
		"behavior":   behavior,
	})

	log.Printf("Permission verdict sent: id=%s behavior=%s", requestID, behavior)
}

// PushMessage sends a Matrix message notification to Claude
func (s *MCPServer) PushMessage(roomID, threadID, sender, eventID, body string) error {
	meta := map[string]string{
		"room_id":  roomID,
		"sender":   sender,
		"event_id": eventID,
	}

	if threadID != "" {
		meta["thread_id"] = threadID
	}

	s.sendNotification("notifications/claude/channel", map[string]interface{}{
		"content": body,
		"meta":    meta,
	})

	return nil
}

// sendResponse sends an MCP response
func (s *MCPServer) sendResponse(id interface{}, result interface{}) {
	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	s.writeJSON(resp)
}

// sendError sends an MCP error response
func (s *MCPServer) sendError(id interface{}, code int, message string) {
	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &MCPError{
			Code:    code,
			Message: message,
		},
	}
	s.writeJSON(resp)
}

// sendNotification sends an MCP notification
func (s *MCPServer) sendNotification(method string, params interface{}) {
	notification := MCPNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	s.writeJSON(notification)
}

// writeJSON writes a JSON message to stdout
func (s *MCPServer) writeJSON(v interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("JSON marshal error: %v", err)
		return
	}

	fmt.Fprintln(s.writer, string(data))
}
