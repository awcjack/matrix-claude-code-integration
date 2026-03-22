package session

import (
	"sync"
)

// UserSession represents a user's session mapping to Claude Code
type UserSession struct {
	// Matrix room ID
	RoomID string

	// Matrix thread ID (event ID of the thread root)
	// Empty string means main room (not in a thread)
	ThreadID string

	// Working directory for this session
	WorkingDirectory string

	// Model to use for this session
	Model string

	// System prompt for this session
	SystemPrompt string
}

// SessionKey uniquely identifies a Matrix conversation context
type SessionKey struct {
	RoomID   string
	ThreadID string
}

// Manager manages the mapping between Matrix threads and Claude Code sessions
type Manager struct {
	mu       sync.RWMutex
	sessions map[SessionKey]*UserSession

	// Default settings
	defaultWorkingDir string
	defaultModel      string
	defaultPrompt     string
}

// NewManager creates a new session manager
func NewManager(workingDir, model, systemPrompt string) *Manager {
	return &Manager{
		sessions:          make(map[SessionKey]*UserSession),
		defaultWorkingDir: workingDir,
		defaultModel:      model,
		defaultPrompt:     systemPrompt,
	}
}

// GetOrCreateSession retrieves or creates a session for the given Matrix context
func (m *Manager) GetOrCreateSession(roomID, threadID string) *UserSession {
	key := SessionKey{RoomID: roomID, ThreadID: threadID}

	m.mu.RLock()
	session, exists := m.sessions[key]
	m.mu.RUnlock()

	if exists {
		return session
	}

	// Create a new session
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if session, exists = m.sessions[key]; exists {
		return session
	}

	session = &UserSession{
		RoomID:           roomID,
		ThreadID:         threadID,
		WorkingDirectory: m.defaultWorkingDir,
		Model:            m.defaultModel,
		SystemPrompt:     m.defaultPrompt,
	}

	m.sessions[key] = session
	return session
}

// GetSession retrieves an existing session without creating one
func (m *Manager) GetSession(roomID, threadID string) (*UserSession, bool) {
	key := SessionKey{RoomID: roomID, ThreadID: threadID}
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, exists := m.sessions[key]
	return session, exists
}

// CreateNewSession creates a new session, replacing any existing one
func (m *Manager) CreateNewSession(roomID, threadID string) *UserSession {
	key := SessionKey{RoomID: roomID, ThreadID: threadID}

	m.mu.Lock()
	defer m.mu.Unlock()

	session := &UserSession{
		RoomID:           roomID,
		ThreadID:         threadID,
		WorkingDirectory: m.defaultWorkingDir,
		Model:            m.defaultModel,
		SystemPrompt:     m.defaultPrompt,
	}

	m.sessions[key] = session
	return session
}

// SetModel updates the model for a session
func (m *Manager) SetModel(roomID, threadID, model string) bool {
	key := SessionKey{RoomID: roomID, ThreadID: threadID}
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[key]
	if !exists {
		return false
	}

	session.Model = model
	return true
}

// SetWorkingDirectory updates the working directory for a session
func (m *Manager) SetWorkingDirectory(roomID, threadID, workingDir string) bool {
	key := SessionKey{RoomID: roomID, ThreadID: threadID}
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[key]
	if !exists {
		return false
	}

	session.WorkingDirectory = workingDir
	return true
}

// DeleteSession removes a session mapping
func (m *Manager) DeleteSession(roomID, threadID string) {
	key := SessionKey{RoomID: roomID, ThreadID: threadID}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, key)
}

// ListSessions returns all active sessions for a room
func (m *Manager) ListSessions(roomID string) []*UserSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*UserSession
	for key, session := range m.sessions {
		if key.RoomID == roomID {
			result = append(result, session)
		}
	}
	return result
}

// GetDefaultModel returns the default model
func (m *Manager) GetDefaultModel() string {
	return m.defaultModel
}

// GetDefaultWorkingDirectory returns the default working directory
func (m *Manager) GetDefaultWorkingDirectory() string {
	return m.defaultWorkingDir
}
