package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// Client connects to the IPC server
type Client struct {
	socketPath   string
	conn         net.Conn
	replyHandler func(ctx context.Context, reply *ReplyPayload)
	mu           sync.Mutex
	connected    bool
}

// NewClient creates a new IPC client
func NewClient(socketPath string, replyHandler func(ctx context.Context, reply *ReplyPayload)) *Client {
	return &Client{
		socketPath:   socketPath,
		replyHandler: replyHandler,
	}
}

// Connect connects to the IPC server with retries
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return nil
	}

	var lastErr error
	for i := 0; i < 30; i++ { // Retry for up to 30 seconds
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := net.Dial("unix", c.socketPath)
		if err != nil {
			lastErr = err
			log.Printf("IPC: waiting for server... (%d/30)", i+1)
			time.Sleep(time.Second)
			continue
		}

		c.conn = conn
		c.connected = true
		log.Printf("IPC: connected to %s", c.socketPath)

		// Start reading replies
		go c.readLoop(ctx)

		return nil
	}

	return fmt.Errorf("failed to connect to IPC server after 30 attempts: %w", lastErr)
}

func (c *Client) readLoop(ctx context.Context) {
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("IPC client: failed to unmarshal message: %v", err)
			continue
		}

		switch msg.Type {
		case TypeReply:
			var payload ReplyPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("IPC client: failed to unmarshal reply payload: %v", err)
				continue
			}
			if c.replyHandler != nil {
				c.replyHandler(ctx, &payload)
			}
		default:
			log.Printf("IPC client: unknown message type: %s", msg.Type)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("IPC client: scanner error: %v", err)
	}

	c.mu.Lock()
	c.connected = false
	c.mu.Unlock()
}

// SendEvent sends a Matrix event to the IPC server
func (c *Client) SendEvent(event *MatrixEventPayload) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected || c.conn == nil {
		return fmt.Errorf("not connected to IPC server")
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}

	msg := Message{
		Type:    TypeMatrixEvent,
		Payload: payload,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	data = append(data, '\n')

	if _, err := c.conn.Write(data); err != nil {
		c.connected = false
		return fmt.Errorf("failed to send event: %w", err)
	}

	return nil
}

// Close closes the client connection
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.connected = false
	}
	return nil
}
