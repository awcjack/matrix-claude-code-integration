package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// Server listens on a Unix socket for IPC connections
type Server struct {
	socketPath string
	listener   net.Listener
	handler    func(ctx context.Context, event *MatrixEventPayload)
	clients    map[net.Conn]struct{}
	clientsMu  sync.RWMutex
}

// NewServer creates a new IPC server
func NewServer(socketPath string, handler func(ctx context.Context, event *MatrixEventPayload)) *Server {
	return &Server{
		socketPath: socketPath,
		handler:    handler,
		clients:    make(map[net.Conn]struct{}),
	}
}

// DefaultSocketPath returns the default socket path
func DefaultSocketPath() string {
	// Use XDG_RUNTIME_DIR if available, otherwise /tmp
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = "/tmp"
	}
	return filepath.Join(runtimeDir, "matrix-claude-code.sock")
}

// Start starts the IPC server
func (s *Server) Start(ctx context.Context) error {
	// Remove existing socket file
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing socket: %w", err)
	}

	// Create socket directory if needed
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0755); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on socket: %w", err)
	}
	s.listener = listener

	// Set socket permissions
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		log.Printf("Warning: failed to set socket permissions: %v", err)
	}

	log.Printf("IPC server listening on %s", s.socketPath)

	go func() {
		<-ctx.Done()
		s.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("IPC accept error: %v", err)
				continue
			}
		}

		s.clientsMu.Lock()
		s.clients[conn] = struct{}{}
		s.clientsMu.Unlock()

		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer func() {
		conn.Close()
		s.clientsMu.Lock()
		delete(s.clients, conn)
		s.clientsMu.Unlock()
	}()

	log.Printf("IPC client connected")

	scanner := bufio.NewScanner(conn)
	// Increase buffer size for large messages
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
			log.Printf("IPC: failed to unmarshal message: %v", err)
			continue
		}

		switch msg.Type {
		case TypeMatrixEvent:
			var payload MatrixEventPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("IPC: failed to unmarshal event payload: %v", err)
				continue
			}
			if s.handler != nil {
				s.handler(ctx, &payload)
			}
		default:
			log.Printf("IPC: unknown message type: %s", msg.Type)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("IPC: scanner error: %v", err)
	}
}

// BroadcastReply sends a reply to all connected clients
func (s *Server) BroadcastReply(reply *ReplyPayload) error {
	payload, err := json.Marshal(reply)
	if err != nil {
		return fmt.Errorf("failed to marshal reply payload: %w", err)
	}

	msg := Message{
		Type:    TypeReply,
		Payload: payload,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	data = append(data, '\n')

	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()

	for conn := range s.clients {
		if _, err := conn.Write(data); err != nil {
			log.Printf("IPC: failed to send reply to client: %v", err)
		}
	}

	return nil
}

// Close closes the server
func (s *Server) Close() error {
	if s.listener != nil {
		s.listener.Close()
	}
	os.Remove(s.socketPath)
	return nil
}
