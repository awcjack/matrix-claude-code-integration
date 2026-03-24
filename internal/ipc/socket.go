package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
)

const (
	// DefaultSocketPath is the default path for the Unix socket
	DefaultSocketPath = "/tmp/matrix-claude-code.sock"
)

// Message represents a message sent over the IPC socket
type Message struct {
	Type     string            `json:"type"`     // "event" or "reply"
	RoomID   string            `json:"room_id"`
	ThreadID string            `json:"thread_id,omitempty"`
	Sender   string            `json:"sender,omitempty"`
	EventID  string            `json:"event_id,omitempty"`
	Content  string            `json:"content"`
	Meta     map[string]string `json:"meta,omitempty"`
}

// Server listens on a Unix socket and forwards messages to a handler
type Server struct {
	socketPath string
	listener   net.Listener
	handler    func(ctx context.Context, msg *Message)
	mu         sync.Mutex
	clients    map[net.Conn]bool
}

// NewServer creates a new IPC server
func NewServer(socketPath string, handler func(ctx context.Context, msg *Message)) *Server {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	return &Server{
		socketPath: socketPath,
		handler:    handler,
		clients:    make(map[net.Conn]bool),
	}
}

// Start starts the IPC server
func (s *Server) Start(ctx context.Context) error {
	// Remove existing socket file
	os.Remove(s.socketPath)

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on socket: %w", err)
	}
	s.listener = listener

	log.Printf("IPC server listening on %s", s.socketPath)

	go func() {
		<-ctx.Done()
		s.listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("Accept error: %v", err)
			continue
		}

		s.mu.Lock()
		s.clients[conn] = true
		s.mu.Unlock()

		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
	}()

	reader := bufio.NewReader(conn)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("Invalid IPC message: %v", err)
			continue
		}

		if s.handler != nil {
			s.handler(ctx, &msg)
		}
	}
}

// Broadcast sends a message to all connected clients
func (s *Server) Broadcast(msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	for conn := range s.clients {
		_, err := conn.Write(data)
		if err != nil {
			log.Printf("Failed to send to client: %v", err)
			conn.Close()
			delete(s.clients, conn)
		}
	}
	return nil
}

// Client connects to an IPC server
type Client struct {
	socketPath string
	conn       net.Conn
	handler    func(ctx context.Context, msg *Message)
	mu         sync.Mutex
}

// NewClient creates a new IPC client
func NewClient(socketPath string, handler func(ctx context.Context, msg *Message)) *Client {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	return &Client{
		socketPath: socketPath,
		handler:    handler,
	}
}

// Connect connects to the IPC server
func (c *Client) Connect(ctx context.Context) error {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to IPC server: %w", err)
	}
	c.conn = conn

	log.Printf("Connected to IPC server at %s", c.socketPath)

	// Start reading messages
	go c.readLoop(ctx)

	return nil
}

func (c *Client) readLoop(ctx context.Context) {
	reader := bufio.NewReader(c.conn)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("IPC read error: %v", err)
			return
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("Invalid IPC message: %v", err)
			continue
		}

		if c.handler != nil {
			c.handler(ctx, &msg)
		}
	}
}

// Send sends a message to the IPC server
func (c *Client) Send(msg *Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("not connected")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	_, err = c.conn.Write(data)
	return err
}

// Close closes the connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
