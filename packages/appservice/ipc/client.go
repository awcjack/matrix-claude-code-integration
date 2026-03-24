package ipc

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"
)

// Client connects to the coordinator's IPC server
type Client struct {
	socketPath string
	sessionID  string
	conn       *Connection

	// Message handlers
	onMessage           func(*IPCMessage) error
	onPermissionVerdict func(requestID string, allowed bool) error
	onShutdown          func(reason string, timeout int) error
}

// NewClient creates a new IPC client for a bridge process
func NewClient(socketPath, sessionID string) *Client {
	return &Client{
		socketPath: socketPath,
		sessionID:  sessionID,
	}
}

// SetMessageHandler sets the handler for Matrix messages
func (c *Client) SetMessageHandler(handler func(*IPCMessage) error) {
	c.onMessage = handler
}

// SetPermissionVerdictHandler sets the handler for permission verdicts
func (c *Client) SetPermissionVerdictHandler(handler func(requestID string, allowed bool) error) {
	c.onPermissionVerdict = handler
}

// SetShutdownHandler sets the handler for shutdown requests
func (c *Client) SetShutdownHandler(handler func(reason string, timeout int) error) {
	c.onShutdown = handler
}

// Connect establishes connection to the coordinator
func (c *Client) Connect(ctx context.Context) error {
	// Retry connection with backoff
	var conn net.Conn
	var err error

	for retries := 0; retries < 5; retries++ {
		conn, err = net.Dial("unix", c.socketPath)
		if err == nil {
			break
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(retries+1) * 100 * time.Millisecond):
		}
	}

	if err != nil {
		return fmt.Errorf("connect to coordinator: %w", err)
	}

	c.conn = NewConnection(conn)

	// Send initial status message
	statusMsg, err := NewIPCMessage(TypeStatus, c.sessionID, &StatusPayload{
		Status: "ready",
	})
	if err != nil {
		c.conn.Close()
		return fmt.Errorf("create status message: %w", err)
	}

	if err := c.conn.SendMessage(statusMsg); err != nil {
		c.conn.Close()
		return fmt.Errorf("send status message: %w", err)
	}

	log.Printf("Connected to coordinator IPC: %s", c.socketPath)
	return nil
}

// Run starts the message receive loop
func (c *Client) Run(ctx context.Context) error {
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msg, err := c.conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read message: %w", err)
		}

		if err := c.handleMessage(msg); err != nil {
			log.Printf("Handle message error: %v", err)
		}
	}
}

// handleMessage dispatches messages to appropriate handlers
func (c *Client) handleMessage(msg *IPCMessage) error {
	switch msg.Type {
	case TypeMatrixMessage:
		if c.onMessage != nil {
			return c.onMessage(msg)
		}

	case TypePermissionVerdict:
		if c.onPermissionVerdict != nil {
			var payload PermissionVerdictPayload
			if err := msg.ParsePayload(&payload); err != nil {
				return fmt.Errorf("parse permission verdict: %w", err)
			}
			return c.onPermissionVerdict(payload.RequestID, payload.Allowed)
		}

	case TypeShutdown:
		if c.onShutdown != nil {
			var payload ShutdownPayload
			if err := msg.ParsePayload(&payload); err != nil {
				return fmt.Errorf("parse shutdown: %w", err)
			}
			return c.onShutdown(payload.Reason, payload.Timeout)
		}

	case TypePing:
		// Respond with pong
		return c.SendPong()

	default:
		log.Printf("Unknown message type: %s", msg.Type)
	}

	return nil
}

// SendReply sends a reply message to the coordinator
func (c *Client) SendReply(roomID, threadID, text string) error {
	msg, err := NewIPCMessage(TypeReply, c.sessionID, &ReplyPayload{
		RoomID:   roomID,
		ThreadID: threadID,
		Text:     text,
	})
	if err != nil {
		return err
	}
	return c.conn.SendMessage(msg)
}

// SendStreamingReply sends a streaming reply chunk to the coordinator
func (c *Client) SendStreamingReply(roomID, threadID, chunk string, isFinal bool) error {
	msg, err := NewIPCMessage(TypeStreamingReply, c.sessionID, &StreamingReplyPayload{
		RoomID:   roomID,
		ThreadID: threadID,
		Chunk:    chunk,
		IsFinal:  isFinal,
	})
	if err != nil {
		return err
	}
	return c.conn.SendMessage(msg)
}

// SendPermissionRequest sends a permission request to the coordinator
func (c *Client) SendPermissionRequest(requestID, toolName, description, inputPreview, roomID string) error {
	msg, err := NewIPCMessage(TypePermissionRequest, c.sessionID, &PermissionRequestPayload{
		RequestID:    requestID,
		ToolName:     toolName,
		Description:  description,
		InputPreview: inputPreview,
		RoomID:       roomID,
	})
	if err != nil {
		return err
	}
	return c.conn.SendMessage(msg)
}

// SendStatus sends a status update to the coordinator
func (c *Client) SendStatus(status, errorMsg string, claudePID int) error {
	msg, err := NewIPCMessage(TypeStatus, c.sessionID, &StatusPayload{
		Status:    status,
		Error:     errorMsg,
		ClaudePID: claudePID,
	})
	if err != nil {
		return err
	}
	return c.conn.SendMessage(msg)
}

// SendPong responds to a ping
func (c *Client) SendPong() error {
	msg, err := NewIPCMessage(TypePong, c.sessionID, &PongPayload{})
	if err != nil {
		return err
	}
	return c.conn.SendMessage(msg)
}

// Close closes the connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
