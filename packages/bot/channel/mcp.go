package channel

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

// MCPServer implements the MCP protocol for Claude Code Channels
// This is a two-way channel that allows Matrix messages to be pushed
// to Claude Code and Claude's replies to be sent back to Matrix.
type MCPServer struct {
	name         string
	version      string
	instructions string

	// Callbacks for reply handling
	replyHandler ReplyHandler

	// Callback for permission requests
	permissionHandler PermissionHandler

	// Message queue for sending notifications
	notifyMu     sync.Mutex
	notifyChan   chan *Notification
	replyResults map[int]chan *ToolResult

	// Request ID counter
	requestID int
	idMu      sync.Mutex

	// Stdio streams
	reader *bufio.Reader
	writer io.Writer
}

// ReplyHandler is called when Claude replies through the channel
type ReplyHandler func(ctx context.Context, roomID, threadID, message string) error

// PermissionHandler is called when Claude Code requests permission to run a tool
// Returns true to allow, false to deny
type PermissionHandler func(ctx context.Context, req *PermissionRequest) (bool, error)

// PermissionRequest represents a permission request from Claude Code
type PermissionRequest struct {
	RequestID    string `json:"request_id"`
	ToolName     string `json:"tool_name"`
	Description  string `json:"description"`
	InputPreview string `json:"input_preview"`
}

// PermissionVerdict represents the response to send back to Claude Code
type PermissionVerdict struct {
	RequestID string `json:"request_id"`
	Behavior  string `json:"behavior"` // "allow" or "deny"
}

// Notification represents a channel notification to send to Claude Code
type Notification struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// ToolResult represents the result of a tool call
type ToolResult struct {
	Content []ContentPart `json:"content"`
}

// ContentPart represents a content part in tool results
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
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

// ServerInfo represents the server capabilities response
type ServerInfo struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      ServerMeta   `json:"serverInfo"`
	Capabilities    Capabilities `json:"capabilities"`
	Instructions    string       `json:"instructions,omitempty"`
}

// ServerMeta contains server metadata
type ServerMeta struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Capabilities declares server capabilities
type Capabilities struct {
	Experimental map[string]interface{} `json:"experimental,omitempty"`
	Tools        map[string]interface{} `json:"tools,omitempty"`
}

// ToolDefinition defines a tool for Claude to use
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema defines the input schema for a tool
type InputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Required   []string               `json:"required"`
}

// NewMCPServer creates a new MCP server for the Matrix channel using stdin/stdout
func NewMCPServer(name, version string, replyHandler ReplyHandler, permissionHandler PermissionHandler) *MCPServer {
	return NewMCPServerWithIO(name, version, replyHandler, permissionHandler, os.Stdin, os.Stdout)
}

// NewMCPServerWithIO creates a new MCP server for the Matrix channel with custom IO
func NewMCPServerWithIO(name, version string, replyHandler ReplyHandler, permissionHandler PermissionHandler, reader io.Reader, writer io.Writer) *MCPServer {
	return &MCPServer{
		name:    name,
		version: version,
		instructions: `Messages from Matrix arrive as <channel source="matrix" room_id="..." sender="..." thread_id="...">.
Reply to Matrix messages using the 'reply' tool, passing the room_id and optionally thread_id from the original message.
Each Matrix thread maintains context - replies in threads should reference the thread_id.`,
		replyHandler:      replyHandler,
		permissionHandler: permissionHandler,
		notifyChan:        make(chan *Notification, 100),
		replyResults:      make(map[int]chan *ToolResult),
		reader:            bufio.NewReader(reader),
		writer:            writer,
	}
}

// Run starts the MCP server, reading from stdin and writing to stdout
func (s *MCPServer) Run(ctx context.Context) error {
	log.Printf("MCP Channel server starting: %s v%s", s.name, s.version)

	// Start notification sender goroutine
	go s.notificationSender(ctx)

	// Process incoming requests
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read error: %w", err)
		}

		if line == "" || line == "\n" {
			continue
		}

		var req MCPRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			log.Printf("Invalid JSON request: %v", err)
			continue
		}

		s.handleRequest(ctx, &req)
	}
}

// handleRequest processes an incoming MCP request
func (s *MCPServer) handleRequest(ctx context.Context, req *MCPRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "initialized":
		// Client acknowledges initialization
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
	info := ServerInfo{
		ProtocolVersion: "2024-11-05",
		ServerInfo: ServerMeta{
			Name:    s.name,
			Version: s.version,
		},
		Capabilities: Capabilities{
			Experimental: map[string]interface{}{
				"claude/channel":            map[string]interface{}{},
				"claude/channel/permission": map[string]interface{}{},
			},
			Tools: map[string]interface{}{},
		},
		Instructions: s.instructions,
	}

	s.sendResponse(req.ID, info)
}

// handleListTools responds with available tools
func (s *MCPServer) handleListTools(req *MCPRequest) {
	tools := []ToolDefinition{
		{
			Name:        "reply",
			Description: "Send a reply back to the Matrix room/thread",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]interface{}{
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
				Required: []string{"room_id", "text"},
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

	// Call the reply handler to send the message back to Matrix
	if s.replyHandler != nil {
		if err := s.replyHandler(ctx, replyArgs.RoomID, replyArgs.ThreadID, replyArgs.Text); err != nil {
			log.Printf("Reply handler error: %v", err)
			s.sendResponse(req.ID, ToolResult{
				Content: []ContentPart{{Type: "text", Text: fmt.Sprintf("failed: %v", err)}},
			})
			return
		}
	}

	s.sendResponse(req.ID, ToolResult{
		Content: []ContentPart{{Type: "text", Text: "sent"}},
	})
}

// handlePermissionRequest processes permission requests from Claude Code
func (s *MCPServer) handlePermissionRequest(ctx context.Context, req *MCPRequest) {
	var permReq PermissionRequest
	if err := json.Unmarshal(req.Params, &permReq); err != nil {
		log.Printf("Invalid permission request: %v", err)
		return
	}

	log.Printf("Permission request: id=%s tool=%s desc=%s",
		permReq.RequestID, permReq.ToolName, permReq.Description)

	// Call the permission handler (will be provided by Matrix integration)
	allowed := false
	if s.permissionHandler != nil {
		var err error
		allowed, err = s.permissionHandler(ctx, &permReq)
		if err != nil {
			log.Printf("Permission handler error: %v", err)
			// Default to deny on error
		}
	}

	// Send verdict back to Claude Code
	behavior := "deny"
	if allowed {
		behavior = "allow"
	}

	s.sendNotification("notifications/claude/channel/permission", map[string]interface{}{
		"request_id": permReq.RequestID,
		"behavior":   behavior,
	})

	log.Printf("Permission verdict sent: id=%s behavior=%s", permReq.RequestID, behavior)
}

// PushMessage sends a notification to Claude Code about a new Matrix message
func (s *MCPServer) PushMessage(roomID, threadID, sender, eventID, body string) error {
	meta := map[string]string{
		"room_id":  roomID,
		"sender":   sender,
		"event_id": eventID,
	}

	if threadID != "" {
		meta["thread_id"] = threadID
	}

	notification := &Notification{
		Content: body,
		Meta:    meta,
	}

	select {
	case s.notifyChan <- notification:
		return nil
	default:
		return fmt.Errorf("notification channel full")
	}
}

// notificationSender sends notifications over stdio
func (s *MCPServer) notificationSender(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case notification := <-s.notifyChan:
			s.sendNotification("notifications/claude/channel", map[string]interface{}{
				"content": notification.Content,
				"meta":    notification.Meta,
			})
		}
	}
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
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()

	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("JSON marshal error: %v", err)
		return
	}

	fmt.Fprintln(s.writer, string(data))
}
