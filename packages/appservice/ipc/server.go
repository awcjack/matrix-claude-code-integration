package ipc

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// MessageHandler is called when a message is received from a bridge
type MessageHandler func(sessionID string, msg *IPCMessage) error

// Server manages IPC connections from bridge processes
type Server struct {
	socketPath string
	listener   net.Listener
	handler    MessageHandler

	mu          sync.RWMutex
	connections map[string]*Connection // sessionID -> connection
	lastPong    map[string]time.Time   // sessionID -> last pong time

	// Health check callback
	onSessionDead func(sessionID string)

	ctx    context.Context
	cancel context.CancelFunc
}

// NewServer creates a new IPC server
func NewServer(socketPath string, handler MessageHandler) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		socketPath:  socketPath,
		handler:     handler,
		connections: make(map[string]*Connection),
		lastPong:    make(map[string]time.Time),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// SetSessionDeadHandler sets the callback for when a session is detected as dead
func (s *Server) SetSessionDeadHandler(handler func(sessionID string)) {
	s.onSessionDead = handler
}

// Start begins listening for connections
func (s *Server) Start() error {
	// Remove existing socket file
	if err := os.RemoveAll(s.socketPath); err != nil {
		return fmt.Errorf("remove socket: %w", err)
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = listener

	// Set permissions
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		listener.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	log.Printf("IPC server listening on %s", s.socketPath)

	go s.acceptLoop()
	go s.healthCheckLoop()
	return nil
}

// acceptLoop accepts new connections
func (s *Server) acceptLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		conn, err := s.listener.Accept()
		if err != nil {
			if s.ctx.Err() != nil {
				return // shutting down
			}
			log.Printf("IPC accept error: %v", err)
			continue
		}

		go s.handleConnection(conn)
	}
}

// handleConnection handles a new bridge connection
func (s *Server) handleConnection(conn net.Conn) {
	ipcConn := NewConnection(conn)
	defer ipcConn.Close()

	// First message should be a status message with session ID
	msg, err := ipcConn.ReadMessage()
	if err != nil {
		log.Printf("IPC read initial message error: %v", err)
		return
	}

	if msg.Type != TypeStatus {
		log.Printf("IPC expected status message, got %s", msg.Type)
		return
	}

	sessionID := msg.SessionID
	if sessionID == "" {
		log.Printf("IPC status message missing session_id")
		return
	}

	// Register connection
	s.mu.Lock()
	s.connections[sessionID] = ipcConn
	s.mu.Unlock()

	log.Printf("IPC session connected: %s", sessionID)

	// Handle the initial status message
	if err := s.handler(sessionID, msg); err != nil {
		log.Printf("IPC handler error for %s: %v", sessionID, err)
	}

	// Read messages until connection closes
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		msg, err := ipcConn.ReadMessage()
		if err != nil {
			log.Printf("IPC session %s disconnected: %v", sessionID, err)
			break
		}

		if err := s.handler(sessionID, msg); err != nil {
			log.Printf("IPC handler error for %s: %v", sessionID, err)
		}
	}

	// Cleanup
	s.mu.Lock()
	delete(s.connections, sessionID)
	s.mu.Unlock()
}

// SendToSession sends a message to a specific session
func (s *Server) SendToSession(sessionID string, msg *IPCMessage) error {
	s.mu.RLock()
	conn, exists := s.connections[sessionID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not connected: %s", sessionID)
	}

	return conn.SendMessage(msg)
}

// healthCheckLoop periodically pings all connected sessions
func (s *Server) healthCheckLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkSessionHealth()
		}
	}
}

// checkSessionHealth sends pings and checks for dead sessions
func (s *Server) checkSessionHealth() {
	s.mu.RLock()
	sessionIDs := make([]string, 0, len(s.connections))
	for id := range s.connections {
		sessionIDs = append(sessionIDs, id)
	}
	s.mu.RUnlock()

	for _, sessionID := range sessionIDs {
		// Check if last pong is too old (missed 2 pings = 30+ seconds)
		s.mu.RLock()
		lastPong, hasPong := s.lastPong[sessionID]
		s.mu.RUnlock()

		if hasPong && time.Since(lastPong) > 35*time.Second {
			log.Printf("Session %s failed health check (no pong for %v)", sessionID, time.Since(lastPong))
			s.markSessionDead(sessionID)
			continue
		}

		// Send ping
		pingMsg, err := NewIPCMessage(TypePing, "", &PingPayload{})
		if err != nil {
			continue
		}

		if err := s.SendToSession(sessionID, pingMsg); err != nil {
			log.Printf("Failed to send ping to session %s: %v", sessionID, err)
			s.markSessionDead(sessionID)
		}
	}
}

// markSessionDead handles a dead session
func (s *Server) markSessionDead(sessionID string) {
	s.mu.Lock()
	conn, exists := s.connections[sessionID]
	if exists {
		conn.Close()
		delete(s.connections, sessionID)
		delete(s.lastPong, sessionID)
	}
	s.mu.Unlock()

	if exists && s.onSessionDead != nil {
		s.onSessionDead(sessionID)
	}
}

// RecordPong records a pong received from a session
func (s *Server) RecordPong(sessionID string) {
	s.mu.Lock()
	s.lastPong[sessionID] = time.Now()
	s.mu.Unlock()
}

// GetConnectedSessions returns a list of connected session IDs
func (s *Server) GetConnectedSessions() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sessions := make([]string, 0, len(s.connections))
	for id := range s.connections {
		sessions = append(sessions, id)
	}
	return sessions
}

// IsSessionConnected checks if a session is connected
func (s *Server) IsSessionConnected(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.connections[sessionID]
	return exists
}

// Stop shuts down the IPC server
func (s *Server) Stop() error {
	s.cancel()

	// Send shutdown to all connections
	s.mu.Lock()
	for _, conn := range s.connections {
		shutdownMsg, _ := NewIPCMessage(TypeShutdown, "", &ShutdownPayload{
			Reason:  "coordinator shutdown",
			Timeout: 30,
		})
		conn.SendMessage(shutdownMsg)
		conn.Close()
	}
	s.connections = make(map[string]*Connection)
	s.mu.Unlock()

	if s.listener != nil {
		s.listener.Close()
	}

	// Remove socket file
	os.RemoveAll(s.socketPath)

	return nil
}
