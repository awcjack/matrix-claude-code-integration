// Package coordinator implements the parent server that manages sessions
package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/anthropics/matrix-claude-code/appservice/ipc"
)

// ClaudeCredentials represents the structure of ~/.claude/.credentials.json
type ClaudeCredentials struct {
	ClaudeAiOauth *struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

// readClaudeOAuthToken reads the OAuth token from ~/.claude/.credentials.json
func readClaudeOAuthToken() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}

	credPath := filepath.Join(homeDir, ".claude", ".credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		return "", fmt.Errorf("read credentials file: %w", err)
	}

	var creds ClaudeCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("parse credentials: %w", err)
	}

	if creds.ClaudeAiOauth == nil || creds.ClaudeAiOauth.AccessToken == "" {
		return "", fmt.Errorf("no OAuth token found in credentials")
	}

	return creds.ClaudeAiOauth.AccessToken, nil
}

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

	if exists {
		// Check if session is ready and process is still running
		if session.Status == "ready" && s.isProcessAlive(session) {
			session.LastActive = time.Now()
			return session, nil
		}
		// If session is starting, check if it's been stuck too long
		if session.Status == "starting" {
			if time.Since(session.CreatedAt) > 60*time.Second {
				log.Printf("Session %s stuck in starting state, cleaning up", sessionID)
				s.mu.Lock()
				s.cleanupSession(session)
				delete(s.sessions, sessionID)
				s.mu.Unlock()
			} else {
				// Still starting, return it so caller can wait
				return session, nil
			}
		}
		// Session is stopped/error/dead - will be cleaned up and respawned
		if session.Status == "dead" {
			log.Printf("Session %s is dead, respawning...", sessionID)
		}
	}

	return s.SpawnSession(roomID, threadID)
}

// isProcessAlive checks if the session's process is still running
func (s *Spawner) isProcessAlive(session *Session) bool {
	if session.Process == nil || session.Process.Process == nil {
		return false
	}
	// Check if process has exited by sending signal 0
	err := session.Process.Process.Signal(syscall.Signal(0))
	return err == nil
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

	// Read OAuth token from ~/.claude/.credentials.json
	// This prevents race conditions when multiple Claude processes access the credential store
	oauthToken, err := readClaudeOAuthToken()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("read oauth token: %w", err)
	}

	// Spawn Claude Code process with MCP channel server
	// The bridge binary acts as an MCP channel server that Claude Code connects to
	// Permissions are handled via Matrix (!allow/!deny) through the MCP channel
	//
	// Flags explained:
	// --dangerously-skip-permissions: Required for non-interactive/headless mode
	// --dangerously-load-development-channels: Required for custom channel servers
	// --channels server:<path>: Connect to our bridge as MCP channel server
	// --print: Run in non-interactive mode (required for headless operation)
	// --output-format stream-json: Output as streaming JSON for parsing
	// --verbose: Enable verbose logging for debugging
	//
	// Note: --print mode requires a prompt argument. We provide an initial prompt
	// that establishes the session. Subsequent messages come via the MCP channel.
	initialPrompt := "Session initialized. Waiting for user input via MCP channel."
	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--dangerously-skip-permissions",
		"--dangerously-load-development-channels",
		"--channels", fmt.Sprintf("server:%s", s.bridgePath),
		"--model", session.Config.Model,
		"--output-format", "stream-json",
		"--verbose",
		initialPrompt,
	)

	// Set working directory
	if session.Config.WorkingDirectory != "" {
		cmd.Dir = session.Config.WorkingDirectory
	}

	// Set environment for bridge to find coordinator
	// Environment variables explained:
	// - CLAUDE_CODE_OAUTH_TOKEN: Bypasses credential store, prevents logout issues
	//   when multiple Claude processes run concurrently
	// - CLAUDE_CODE_HOST_PLATFORM: Override platform reported in telemetry
	// - CLAUDE_CODE_ENTRYPOINT: Mark as standard CLI entrypoint
	// - BRIDGE_*: Internal variables for bridge IPC communication
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CLAUDE_CODE_OAUTH_TOKEN=%s", oauthToken),
		"CLAUDE_CODE_HOST_PLATFORM=linux",
		"CLAUDE_CODE_ENTRYPOINT=cli",
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

	// Wait for process exit in a goroutine so we can also watch for context cancellation
	done := make(chan error, 1)
	go func() {
		done <- session.Process.Wait()
	}()

	select {
	case <-s.ctx.Done():
		// Spawner is shutting down - kill the process if still running
		if session.Process.Process != nil {
			session.Process.Process.Kill()
		}
		<-done // Wait for process to exit
		return
	case err := <-done:
		// Process exited naturally
		s.mu.Lock()
		defer s.mu.Unlock()

		// Check if session still exists (might have been cleaned up already)
		if _, exists := s.sessions[session.ID]; !exists {
			return
		}

		if err != nil {
			log.Printf("Session %s exited with error: %v", session.ID, err)
			session.Status = "error"
		} else {
			log.Printf("Session %s exited cleanly", session.ID)
			session.Status = "stopped"
		}
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

// HandleDeadSession handles a session that has been detected as dead by health check.
// It marks the session for recovery so the next message will respawn it.
func (s *Spawner) HandleDeadSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[sessionID]
	if !exists {
		return
	}

	log.Printf("Session %s detected as dead, marking for recovery", sessionID)

	// Clean up the dead process
	if session.cancel != nil {
		session.cancel()
	}
	if session.Process != nil && session.Process.Process != nil {
		// Try to kill if still running
		session.Process.Process.Kill()
	}

	// Mark as dead - next GetOrCreateSession will respawn
	session.Status = "dead"
	session.Process = nil
}

// RespawnDeadSession attempts to respawn a dead session immediately.
// Returns the new session if successful.
func (s *Spawner) RespawnDeadSession(sessionID string) (*Session, error) {
	s.mu.RLock()
	session, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	if session.Status != "dead" {
		return nil, fmt.Errorf("session %s is not dead (status: %s)", sessionID, session.Status)
	}

	// Respawn by calling SpawnSession which will clean up and create new
	log.Printf("Respawning dead session %s for room %s", sessionID, session.RoomID)
	return s.SpawnSession(session.RoomID, session.ThreadID)
}
