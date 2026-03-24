// Package coordinator implements the parent server that manages sessions
package coordinator

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/anthropics/matrix-claude-code/appservice/ipc"
)

// SessionConfig holds configuration for a session
type SessionConfig struct {
	WorkingDirectory string
	Model            string
	SystemPrompt     string
	MaxTurns         int
}

// Session represents an active Claude Code session
type Session struct {
	ID           string
	RoomID       string
	ThreadID     string
	Config       SessionConfig
	Status       string
	Process      *exec.Cmd
	CreatedAt    time.Time
	LastActive   time.Time
	MessageCount int

	cancel context.CancelFunc
}

// Spawner manages the lifecycle of bridge processes
type Spawner struct {
	bridgePath   string
	socketPath   string
	defaultConfig SessionConfig

	mu       sync.RWMutex
	sessions map[string]*Session // sessionID -> session

	ctx    context.Context
	cancel context.CancelFunc
}

// NewSpawner creates a new session spawner
func NewSpawner(bridgePath, socketPath string, defaultConfig SessionConfig) *Spawner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Spawner{
		bridgePath:    bridgePath,
		socketPath:    socketPath,
		defaultConfig: defaultConfig,
		sessions:      make(map[string]*Session),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// GetOrCreateSession returns an existing session or creates a new one
func (s *Spawner) GetOrCreateSession(roomID, threadID string) (*Session, error) {
	sessionID := s.makeSessionID(roomID, threadID)

	s.mu.RLock()
	session, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	if exists && session.Status == "ready" {
		session.LastActive = time.Now()
		return session, nil
	}

	return s.SpawnSession(roomID, threadID)
}

// SpawnSession creates a new bridge process for a session
func (s *Spawner) SpawnSession(roomID, threadID string) (*Session, error) {
	sessionID := s.makeSessionID(roomID, threadID)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check again under write lock
	if existing, exists := s.sessions[sessionID]; exists {
		if existing.Status == "ready" {
			existing.LastActive = time.Now()
			return existing, nil
		}
		// Clean up dead session
		s.cleanupSession(existing)
	}

	// Create session context
	ctx, cancel := context.WithCancel(s.ctx)

	session := &Session{
		ID:        sessionID,
		RoomID:    roomID,
		ThreadID:  threadID,
		Config:    s.defaultConfig,
		Status:    "starting",
		CreatedAt: time.Now(),
		LastActive: time.Now(),
		cancel:    cancel,
	}

	// Spawn bridge process
	// The bridge will be started by Claude Code as its MCP channel server
	// We just need to track it and wait for it to connect via IPC
	cmd := exec.CommandContext(ctx, "claude",
		"--channels", fmt.Sprintf("server:%s", s.bridgePath),
		"--model", session.Config.Model,
	)

	// Set working directory
	if session.Config.WorkingDirectory != "" {
		cmd.Dir = session.Config.WorkingDirectory
	}

	// Set environment for bridge to find coordinator
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BRIDGE_SESSION_ID=%s", sessionID),
		fmt.Sprintf("BRIDGE_IPC_SOCKET=%s", s.socketPath),
		fmt.Sprintf("BRIDGE_ROOM_ID=%s", roomID),
		fmt.Sprintf("BRIDGE_THREAD_ID=%s", threadID),
	)

	// Capture output for debugging
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	session.Process = cmd
	s.sessions[sessionID] = session

	// Monitor process in background
	go s.monitorProcess(session)

	log.Printf("Spawned session %s (PID %d) for room %s", sessionID, cmd.Process.Pid, roomID)

	return session, nil
}

// monitorProcess watches a session process and cleans up when it exits
func (s *Spawner) monitorProcess(session *Session) {
	if session.Process == nil {
		return
	}

	err := session.Process.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		log.Printf("Session %s exited with error: %v", session.ID, err)
		session.Status = "error"
	} else {
		log.Printf("Session %s exited cleanly", session.ID)
		session.Status = "stopped"
	}
}

// GetSession returns a session by ID
func (s *Spawner) GetSession(sessionID string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, exists := s.sessions[sessionID]
	return session, exists
}

// GetSessionByRoom returns a session for a room/thread
func (s *Spawner) GetSessionByRoom(roomID, threadID string) (*Session, bool) {
	sessionID := s.makeSessionID(roomID, threadID)
	return s.GetSession(sessionID)
}

// UpdateSessionStatus updates the status of a session
func (s *Spawner) UpdateSessionStatus(sessionID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, exists := s.sessions[sessionID]; exists {
		session.Status = status
		session.LastActive = time.Now()
	}
}

// IncrementMessageCount increments the message count for a session
func (s *Spawner) IncrementMessageCount(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, exists := s.sessions[sessionID]; exists {
		session.MessageCount++
		session.LastActive = time.Now()
	}
}

// ListSessions returns info about all sessions
func (s *Spawner) ListSessions() []ipc.SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ipc.SessionInfo, 0, len(s.sessions))
	for _, session := range s.sessions {
		result = append(result, ipc.SessionInfo{
			SessionID:    session.ID,
			RoomID:       session.RoomID,
			ThreadID:     session.ThreadID,
			Status:       session.Status,
			CreatedAt:    session.CreatedAt,
			LastActive:   session.LastActive,
			MessageCount: session.MessageCount,
		})
	}
	return result
}

// StopSession stops a specific session
func (s *Spawner) StopSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[sessionID]
	if !exists {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	s.cleanupSession(session)
	delete(s.sessions, sessionID)
	return nil
}

// cleanupSession stops a session's process (must hold write lock)
func (s *Spawner) cleanupSession(session *Session) {
	if session.cancel != nil {
		session.cancel()
	}
	if session.Process != nil && session.Process.Process != nil {
		session.Process.Process.Kill()
	}
	session.Status = "stopped"
}

// StopAllSessions stops all sessions
func (s *Spawner) StopAllSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, session := range s.sessions {
		s.cleanupSession(session)
	}
	s.sessions = make(map[string]*Session)
}

// Shutdown stops the spawner and all sessions
func (s *Spawner) Shutdown() {
	s.cancel()
	s.StopAllSessions()
}

// makeSessionID creates a unique session ID from room and thread
func (s *Spawner) makeSessionID(roomID, threadID string) string {
	if threadID != "" {
		return fmt.Sprintf("%s:%s", roomID, threadID)
	}
	return roomID
}

// CleanupIdleSessions stops sessions that have been idle too long
func (s *Spawner) CleanupIdleSessions(maxIdle time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for id, session := range s.sessions {
		if now.Sub(session.LastActive) > maxIdle {
			log.Printf("Stopping idle session: %s (idle for %v)", id, now.Sub(session.LastActive))
			s.cleanupSession(session)
			delete(s.sessions, id)
		}
	}
}
