// Package coordinator implements the parent server that manages sessions
package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anthropics/matrix-claude-code/appservice/ipc"
)

// ClaudeCredentials represents the structure of ~/.claude/.credentials.json
type ClaudeCredentials struct {
	ClaudeAiOauth *struct {
		AccessToken      string   `json:"accessToken"`
		RefreshToken     string   `json:"refreshToken"`
		ExpiresAt        int64    `json:"expiresAt"`
		Scopes           []string `json:"scopes,omitempty"`
		SubscriptionType string   `json:"subscriptionType,omitempty"`
		RateLimitTier    string   `json:"rateLimitTier,omitempty"`
	} `json:"claudeAiOauth"`
}

// getCredentialsPath returns the path to ~/.claude/.credentials.json
func getCredentialsPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".claude", ".credentials.json"), nil
}

// Note: Manual OAuth token refresh has been removed.
// Claude Code handles its own token refresh internally when spawned.
// If the token is expired, the user must run 'claude login' to re-authenticate.
// Attempting to refresh manually with a hardcoded client ID is unreliable
// as the client ID may change with Claude Code updates.

// readClaudeOAuthToken reads the OAuth token from ~/.claude/.credentials.json
// If the token is expired, it will attempt to refresh it using the refresh token
func readClaudeOAuthToken() (string, error) {
	credPath, err := getCredentialsPath()
	if err != nil {
		return "", err
	}

	log.Printf("Reading OAuth token from: %s", credPath)

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

	// Log token info (first/last 8 chars only for security)
	token := creds.ClaudeAiOauth.AccessToken
	if len(token) > 16 {
		log.Printf("OAuth token loaded: %s...%s (expires: %d)", token[:8], token[len(token)-8:], creds.ClaudeAiOauth.ExpiresAt)
	}

	// Check if token is expired or about to expire (5 minute buffer)
	// Note: We no longer attempt to refresh manually - Claude Code handles this internally.
	// If expired, return an error prompting the user to re-login.
	now := time.Now().UnixMilli()
	bufferMs := int64(5 * 60 * 1000) // 5 minutes
	if creds.ClaudeAiOauth.ExpiresAt > 0 && creds.ClaudeAiOauth.ExpiresAt < (now+bufferMs) {
		log.Printf("OAuth token expired or expiring soon (expiresAt=%d, now=%d)", creds.ClaudeAiOauth.ExpiresAt, now)
		return "", fmt.Errorf("OAuth token expired. Please run 'claude login' in the container to re-authenticate")
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
	Stdin        io.WriteCloser // Stdin pipe to keep process alive
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

	// Ensure the working directory is a git repository
	// Claude Code's workspace trust prompt only appears in non-git directories.
	// By initializing git, we bypass the trust prompt in headless mode.
	workDir := session.Config.WorkingDirectory
	if workDir == "" {
		workDir = "/workspace"
	}
	if err := s.ensureGitRepo(workDir); err != nil {
		log.Printf("Warning: failed to ensure git repo in %s: %v", workDir, err)
		// Continue anyway - the trust prompt will appear but may be handled
	}

	// Create a wrapper script for the bridge that includes session configuration
	// This is needed because Claude Code spawns the channel server as a subprocess
	// without inheriting the coordinator's environment variables.
	wrapperPath, err := s.createBridgeWrapper(sessionID, roomID, threadID)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create bridge wrapper: %w", err)
	}

	// Register the bridge as an MCP server in .mcp.json
	// This is required because Claude Code's --channels server:<name> syntax
	// expects a configured MCP server name, not a file path.
	serverName, err := s.registerBridgeAsMCPServer(wrapperPath, sessionID, session.Config.WorkingDirectory)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("register bridge as MCP server: %w", err)
	}

	// Spawn Claude Code process with MCP channel server
	// The bridge binary acts as an MCP channel server that Claude Code connects to
	// Permissions are handled via Matrix (!allow/!deny) through the MCP channel
	//
	// Flags explained:
	// --dangerously-skip-permissions: Required for non-interactive/headless mode
	// --dangerously-load-development-channels: Required for custom channel servers
	// --channels server:<name>: Connect to our bridge as MCP channel server (name from .mcp.json)
	// --resume: Resume or create a new session (prevents --print mode auto-detection)
	//
	// Note: We run Claude Code with --input-format stream-json and keep stdin open.
	// This allows Claude to receive messages from the MCP channel while running.
	// We provide an initial empty newline to prevent immediate exit.
	channelArg := fmt.Sprintf("server:%s", serverName)
	log.Printf("Spawning Claude with channels: %s (MCP server: %s)", channelArg, serverName)

	// System prompt for Matrix chat mode
	// Important: Explain that channel tools are NOT discovered via ToolSearch - they are provided
	// by the MCP channel server and can be called directly by name
	systemPrompt := `You are connected to a Matrix chat room via an MCP channel.

IMPORTANT: The 'reply' tool is provided by the matrix-bridge channel server, NOT by ToolSearch.
Do NOT use ToolSearch to find the reply tool - it will not be found there.
Instead, call the 'reply' tool directly when you need to respond to messages.

Messages from Matrix will arrive as channel notifications with room_id, sender, and thread_id metadata.
To respond, use the 'reply' tool with these parameters:
- room_id: The Matrix room ID (required)
- text: Your response message (required)
- thread_id: The thread ID if replying in a thread (optional)

Wait for incoming Matrix messages and respond appropriately using the reply tool.`

	// Run Claude Code with script command to provide a PTY
	// Channels require a true interactive session with a TTY.
	// The 'script' command creates a PTY and keeps the session alive.
	//
	// Architecture:
	// 1. script -q /dev/null creates a PTY
	// 2. Claude runs in interactive mode inside the PTY
	// 3. Channel server (bridge) pushes messages via MCP notifications
	// 4. Claude responds and the bridge sends replies back to Matrix
	//
	// We use /dev/null as the typescript file since we don't need to record
	//
	// IMPORTANT: We create a shell script to run Claude instead of passing
	// the command directly to script -c, because the system prompt contains
	// special characters that could be mangled by shell interpretation.
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(sessionID)))[:16]
	claudeScript := filepath.Join(os.TempDir(), fmt.Sprintf("claude-run-%s.sh", hash))
	// NOTE: The channel argument goes directly after --dangerously-load-development-channels,
	// NOT as a separate --channels flag. The syntax is:
	//   claude --dangerously-load-development-channels server:<name>
	// NOT:
	//   claude --dangerously-load-development-channels --channels server:<name>
	//
	// We use 'expect' to automatically respond to interactive prompts:
	// 1. The bypass permissions warning ("Yes, I accept")
	// 2. Any other confirmation prompts
	//
	// The expect script spawns a shell script that runs claude, to avoid
	// Tcl/expect interpreting special characters in the system prompt.
	// The shell script is written separately with the system prompt properly escaped.

	// First, write a shell script that launches claude with the system prompt
	claudeShellScript := filepath.Join(os.TempDir(), fmt.Sprintf("claude-cmd-%s.sh", hash))
	// Escape the system prompt for shell - replace single quotes with '\''
	escapedPrompt := strings.ReplaceAll(systemPrompt, "'", "'\\''")
	shellScriptContent := fmt.Sprintf(`#!/bin/bash
exec claude --dangerously-skip-permissions --dangerously-load-development-channels '%s' --model '%s' --append-system-prompt '%s' --verbose
`, channelArg, session.Config.Model, escapedPrompt)
	if err := os.WriteFile(claudeShellScript, []byte(shellScriptContent), 0755); err != nil {
		cancel()
		return nil, fmt.Errorf("write claude shell script: %w", err)
	}

	// Now create the expect script that spawns the shell script
	// This keeps Tcl from seeing the system prompt content
	claudeScriptContent := fmt.Sprintf(`#!/usr/bin/expect -f
# Auto-accept prompts for headless operation
set timeout -1

# Spawn the shell script that launches claude
spawn %s

# Wait for and respond to the bypass permissions prompt
# The prompt uses ink's select component - need to:
# 1. Press down arrow to move to option 2 ("Yes, I accept")
# 2. Press Enter to confirm
expect {
    "No, exit" {
        # Menu appeared, send down arrow to select option 2, then Enter
        sleep 0.5
        send "\x1b\[B"
        sleep 0.2
        send "\r"
        exp_continue
    }
    "Yes, I accept" {
        # Already on option 2 or confirmation, just press Enter
        sleep 0.2
        send "\r"
        exp_continue
    }
    eof {
        # Claude exited
    }
}

# Keep waiting for more prompts or exit
wait
`, claudeShellScript)
	if err := os.WriteFile(claudeScript, []byte(claudeScriptContent), 0755); err != nil {
		cancel()
		return nil, fmt.Errorf("write claude script: %w", err)
	}

	// Run the expect script directly - expect provides its own PTY
	cmd := exec.CommandContext(ctx, claudeScript)

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

	// Create stdin pipe to send input to Claude's interactive session
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	session.Stdin = stdinPipe

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
	// Close stdin pipe to signal process to exit
	if session.Stdin != nil {
		session.Stdin.Close()
	}
	if session.Process != nil && session.Process.Process != nil {
		session.Process.Process.Kill()
	}
	session.Status = "stopped"

	// Clean up the wrapper script and claude scripts (use same hash as createBridgeWrapper)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(session.ID)))[:16]
	wrapperPath := filepath.Join(os.TempDir(), fmt.Sprintf("matrix-bridge-%s.sh", hash))
	claudeScript := filepath.Join(os.TempDir(), fmt.Sprintf("claude-run-%s.sh", hash))
	claudeShellScript := filepath.Join(os.TempDir(), fmt.Sprintf("claude-cmd-%s.sh", hash))
	os.Remove(wrapperPath)      // Ignore errors - file may not exist
	os.Remove(claudeScript)     // Ignore errors - file may not exist
	os.Remove(claudeShellScript) // Ignore errors - file may not exist

	// Clean up the MCP server entry from ~/.claude.json
	s.unregisterBridgeMCPServer(session.ID, session.Config.WorkingDirectory)
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

// ensureGitRepo initializes a git repository in the given directory if one doesn't exist.
// This is needed because Claude Code's workspace trust prompt only appears in non-git
// directories. By ensuring a git repo exists, we bypass the interactive trust prompt.
func (s *Spawner) ensureGitRepo(dir string) error {
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		// .git already exists
		return nil
	}

	// Initialize a new git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init failed: %w (output: %s)", err, string(output))
	}

	// Configure git user for the repo (required for commits)
	cmd = exec.Command("git", "config", "user.email", "claude@matrix.local")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		log.Printf("Warning: failed to set git email: %v", err)
	}

	cmd = exec.Command("git", "config", "user.name", "Claude Code")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		log.Printf("Warning: failed to set git name: %v", err)
	}

	log.Printf("Initialized git repository in %s to bypass workspace trust prompt", dir)
	return nil
}

// createBridgeWrapper creates a shell script wrapper that launches the bridge
// with the correct configuration. This is needed because Claude Code spawns
// channel servers as subprocesses without inheriting environment variables.
func (s *Spawner) createBridgeWrapper(sessionID, roomID, threadID string) (string, error) {
	// Create wrapper script in /tmp with unique name
	// Use hash of sessionID to avoid special characters in filename (!, :, etc)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(sessionID)))[:16]
	wrapperPath := filepath.Join(os.TempDir(), fmt.Sprintf("matrix-bridge-%s.sh", hash))

	// Create wrapper script content
	// IMPORTANT: Do NOT pipe or redirect stdout - MCP requires bidirectional stdio
	// Only redirect stderr to log file for debugging
	logFile := filepath.Join(os.TempDir(), fmt.Sprintf("matrix-bridge-%s.log", hash))
	script := fmt.Sprintf(`#!/bin/sh
# Bridge wrapper script - created by coordinator
# IMPORTANT: stdout must go directly to Claude for MCP protocol
# Only stderr is redirected for debugging
LOG_FILE="%s"
echo "[$(date)] Bridge wrapper starting: session=%s" >> "$LOG_FILE"
echo "[$(date)] Bridge path: %s" >> "$LOG_FILE"
echo "[$(date)] Socket: %s" >> "$LOG_FILE"

# Check if bridge binary exists
if [ ! -x "%s" ]; then
    echo "[$(date)] ERROR: Bridge binary not found or not executable: %s" >> "$LOG_FILE"
    exit 1
fi

# Execute bridge - stdout goes to Claude, stderr to log file
exec %s --session-id %q --socket %q --room-id %q --thread-id %q 2>>"$LOG_FILE"
`, logFile, sessionID, s.bridgePath, s.socketPath, s.bridgePath, s.bridgePath, s.bridgePath, sessionID, s.socketPath, roomID, threadID)

	// Write the wrapper script
	if err := os.WriteFile(wrapperPath, []byte(script), 0755); err != nil {
		return "", fmt.Errorf("write wrapper script: %w", err)
	}

	log.Printf("Created bridge wrapper at %s for session %s", wrapperPath, sessionID)
	return wrapperPath, nil
}

// ensureBypassPermissionsAccepted creates a ~/.claude/settings.json that pre-accepts
// bypass permissions mode, preventing the interactive confirmation prompt.
func (s *Spawner) ensureBypassPermissionsAccepted() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home directory: %w", err)
	}

	// Create ~/.claude directory if it doesn't exist
	claudeDir := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("create ~/.claude directory: %w", err)
	}

	// Create settings.json with bypassPermissions accepted
	settingsPath := filepath.Join(claudeDir, "settings.json")

	// Read existing settings or create new
	var settings map[string]interface{}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read settings.json: %w", err)
		}
		settings = map[string]interface{}{}
	} else {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse settings.json: %w", err)
		}
	}

	// Ensure permissions section exists and set bypassPermissions mode
	permissions, ok := settings["permissions"].(map[string]interface{})
	if !ok {
		permissions = map[string]interface{}{}
		settings["permissions"] = permissions
	}
	permissions["defaultMode"] = "bypassPermissions"

	// Write back settings.json
	updatedData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings.json: %w", err)
	}

	if err := os.WriteFile(settingsPath, updatedData, 0644); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}

	log.Printf("Configured bypassPermissions mode in %s", settingsPath)
	return nil
}

// registerBridgeAsMCPServer registers the bridge wrapper as an MCP server in ~/.claude.json
// This is required because Claude Code's --channels server:<name> flag requires the
// MCP server to be configured in Claude's settings first.
// We use ~/.claude.json (global config) to ensure the server is found regardless of
// the current working directory.
// Returns the server name to use with --channels server:<name>
func (s *Spawner) registerBridgeAsMCPServer(wrapperPath, sessionID, workDir string) (string, error) {
	// Use a unique server name per session to avoid conflicts
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(sessionID)))[:16]
	serverName := fmt.Sprintf("matrix-bridge-%s", hash)

	// Ensure bypassPermissions mode is pre-accepted to avoid interactive prompt
	if err := s.ensureBypassPermissionsAccepted(); err != nil {
		log.Printf("Warning: failed to pre-accept bypassPermissions: %v", err)
	}

	// Always use ~/.claude.json for global MCP server registration
	// This ensures Claude Code finds the server regardless of working directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	claudeConfigPath := filepath.Join(homeDir, ".claude.json")

	// Determine workspace directory for trust settings
	workspaceDir := workDir
	if workspaceDir == "" {
		workspaceDir = "/workspace"
	}

	// Read existing ~/.claude.json or create new structure
	var claudeConfig map[string]interface{}
	data, err := os.ReadFile(claudeConfigPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read ~/.claude.json: %w", err)
		}
		// Create new config
		claudeConfig = map[string]interface{}{
			"mcpServers": map[string]interface{}{},
		}
	} else {
		if err := json.Unmarshal(data, &claudeConfig); err != nil {
			return "", fmt.Errorf("parse ~/.claude.json: %w", err)
		}
	}

	// Ensure mcpServers key exists
	mcpServers, ok := claudeConfig["mcpServers"].(map[string]interface{})
	if !ok {
		mcpServers = map[string]interface{}{}
		claudeConfig["mcpServers"] = mcpServers
	}

	// Add our bridge server configuration
	// Claude Code expects "command" and optional "args" for MCP servers
	mcpServers[serverName] = map[string]interface{}{
		"command": wrapperPath,
	}

	// Pre-trust the workspace directory to bypass the interactive trust prompt
	// Claude Code stores per-project trust settings in ~/.claude.json under "projects"
	// The structure is: projects[<base64-encoded-path>] = { hasTrustDialogAccepted: true, ... }
	projects, ok := claudeConfig["projects"].(map[string]interface{})
	if !ok {
		projects = map[string]interface{}{}
		claudeConfig["projects"] = projects
	}

	// Encode the workspace path as a key (Claude uses base64 encoding for paths)
	// Actually, looking at typical configs, Claude seems to use the path directly or a hash
	// Let's try adding trust for the workspace directory
	projectKey := workspaceDir
	projectConfig, ok := projects[projectKey].(map[string]interface{})
	if !ok {
		projectConfig = map[string]interface{}{}
		projects[projectKey] = projectConfig
	}
	projectConfig["hasTrustDialogAccepted"] = true
	projectConfig["isTrusted"] = true

	// Write back the updated ~/.claude.json
	updatedData, err := json.MarshalIndent(claudeConfig, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal ~/.claude.json: %w", err)
	}

	if err := os.WriteFile(claudeConfigPath, updatedData, 0644); err != nil {
		return "", fmt.Errorf("write ~/.claude.json: %w", err)
	}

	log.Printf("Registered bridge as MCP server '%s' in %s", serverName, claudeConfigPath)
	log.Printf("Pre-trusted workspace directory '%s' in %s", workspaceDir, claudeConfigPath)
	return serverName, nil
}

// unregisterBridgeMCPServer removes the bridge MCP server entry from ~/.claude.json
// Called during session cleanup to avoid stale entries accumulating.
func (s *Spawner) unregisterBridgeMCPServer(sessionID, workDir string) {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(sessionID)))[:16]
	serverName := fmt.Sprintf("matrix-bridge-%s", hash)

	// Always use ~/.claude.json (matching registerBridgeAsMCPServer)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("Failed to get home directory for cleanup: %v", err)
		return
	}
	claudeConfigPath := filepath.Join(homeDir, ".claude.json")

	// Read existing ~/.claude.json
	data, err := os.ReadFile(claudeConfigPath)
	if err != nil {
		// File doesn't exist, nothing to clean up
		return
	}

	var claudeConfig map[string]interface{}
	if err := json.Unmarshal(data, &claudeConfig); err != nil {
		log.Printf("Failed to parse ~/.claude.json during cleanup: %v", err)
		return
	}

	// Get mcpServers and remove our entry
	mcpServers, ok := claudeConfig["mcpServers"].(map[string]interface{})
	if !ok {
		return
	}

	if _, exists := mcpServers[serverName]; !exists {
		return // Nothing to remove
	}

	delete(mcpServers, serverName)

	// Write back the updated ~/.claude.json
	updatedData, err := json.MarshalIndent(claudeConfig, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal ~/.claude.json during cleanup: %v", err)
		return
	}

	if err := os.WriteFile(claudeConfigPath, updatedData, 0644); err != nil {
		log.Printf("Failed to write ~/.claude.json during cleanup: %v", err)
		return
	}

	log.Printf("Unregistered bridge MCP server '%s' from %s", serverName, claudeConfigPath)
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
