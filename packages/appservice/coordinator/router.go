package coordinator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/matrix-claude-code/appservice/ipc"
	"github.com/anthropics/matrix-claude-code/appservice/matrix"
)

// Router routes Matrix events to appropriate sessions
type Router struct {
	spawner    *Spawner
	ipcServer  *ipc.Server
	client     matrix.MatrixClient
	whitelist  map[string]bool

	// Pending permission requests
	permMu      sync.Mutex
	pendingPerm map[string]*PendingPermission // requestID -> pending

	// Streaming message state
	streamMu        sync.Mutex
	streamingEvents map[string]*StreamingMessage // key: roomID+threadID -> event info
}

// PendingPermission tracks a permission request
type PendingPermission struct {
	SessionID  string
	RequestID  string
	RoomID     string
	ResultChan chan bool
	CreatedAt  time.Time
}

// StreamingMessage tracks an active streaming message
type StreamingMessage struct {
	EventID     string
	Content     string
	LastUpdated time.Time
	CreatedAt   time.Time
}

// NewRouter creates a new event router
func NewRouter(spawner *Spawner, ipcServer *ipc.Server, client matrix.MatrixClient, whitelist []string) *Router {
	wl := make(map[string]bool)
	for _, user := range whitelist {
		wl[user] = true
	}

	return &Router{
		spawner:         spawner,
		ipcServer:       ipcServer,
		client:          client,
		whitelist:       wl,
		pendingPerm:     make(map[string]*PendingPermission),
		streamingEvents: make(map[string]*StreamingMessage),
	}
}

// HandleMatrixEvent processes a Matrix event and routes it to the appropriate session
func (r *Router) HandleMatrixEvent(ctx context.Context, roomID, threadID, sender, eventID, body string, timestamp int64) error {
	// Check whitelist
	if !r.whitelist[sender] {
		log.Printf("Ignoring message from non-whitelisted user: %s", sender)
		return nil
	}

	// Check for permission response commands
	if isPermCmd, reqID, allowed := r.checkPermissionCommand(body); isPermCmd {
		return r.handlePermissionResponse(ctx, roomID, threadID, eventID, reqID, allowed)
	}

	// Check for bot commands
	if strings.HasPrefix(body, "!") {
		return r.handleCommand(ctx, roomID, threadID, eventID, body)
	}

	// Get or create session
	session, err := r.spawner.GetOrCreateSession(roomID, threadID)
	if err != nil {
		log.Printf("Failed to get/create session: %v", err)
		r.sendErrorReply(ctx, roomID, threadID, eventID, "Failed to start Claude Code session")
		return err
	}

	// Wait for session to be ready (with timeout)
	// Increased to 60s to allow time for Claude Code startup and MCP channel initialization
	if session.Status != "ready" {
		if err := r.waitForSession(ctx, session.ID, 60*time.Second); err != nil {
			log.Printf("Session not ready: %v", err)
			r.sendErrorReply(ctx, roomID, threadID, eventID, "Claude Code session is starting up, please wait...")
			return err
		}
	}

	// Forward message via IPC
	msg, err := ipc.NewIPCMessage(ipc.TypeMatrixMessage, session.ID, &ipc.MatrixMessagePayload{
		RoomID:    roomID,
		ThreadID:  threadID,
		Sender:    sender,
		EventID:   eventID,
		Body:      body,
		Timestamp: timestamp,
	})
	if err != nil {
		return fmt.Errorf("create message: %w", err)
	}

	if err := r.ipcServer.SendToSession(session.ID, msg); err != nil {
		log.Printf("Failed to send to session %s: %v", session.ID, err)
		return err
	}

	r.spawner.IncrementMessageCount(session.ID)
	log.Printf("Routed message to session %s", session.ID)
	return nil
}

// HandleIPCMessage processes messages from bridge processes
func (r *Router) HandleIPCMessage(sessionID string, msg *ipc.IPCMessage) error {
	switch msg.Type {
	case ipc.TypeStatus:
		var payload ipc.StatusPayload
		if err := msg.ParsePayload(&payload); err != nil {
			return err
		}
		r.spawner.UpdateSessionStatus(sessionID, payload.Status)
		log.Printf("Session %s status: %s", sessionID, payload.Status)

	case ipc.TypeReply:
		var payload ipc.ReplyPayload
		if err := msg.ParsePayload(&payload); err != nil {
			return err
		}
		return r.handleReply(context.Background(), payload.RoomID, payload.ThreadID, payload.Text)

	case ipc.TypeStreamingReply:
		var payload ipc.StreamingReplyPayload
		if err := msg.ParsePayload(&payload); err != nil {
			return err
		}
		return r.handleStreamingReply(context.Background(), payload.RoomID, payload.ThreadID, payload.Chunk, payload.IsFinal)

	case ipc.TypePermissionRequest:
		var payload ipc.PermissionRequestPayload
		if err := msg.ParsePayload(&payload); err != nil {
			return err
		}
		return r.handlePermissionRequest(context.Background(), sessionID, &payload)

	case ipc.TypePong:
		r.ipcServer.RecordPong(sessionID)
		log.Printf("Session %s pong received", sessionID)

	default:
		log.Printf("Unknown IPC message type from %s: %s", sessionID, msg.Type)
	}

	return nil
}

// handleReply sends Claude's reply to Matrix
func (r *Router) handleReply(ctx context.Context, roomID, threadID, text string) error {
	if threadID != "" {
		_, err := r.client.SendReply(ctx, roomID, threadID, threadID, text, false)
		return err
	}
	_, err := r.client.SendMessage(ctx, roomID, text)
	return err
}

// handleStreamingReply handles streaming reply chunks from Claude Code
func (r *Router) handleStreamingReply(ctx context.Context, roomID, threadID, chunk string, isFinal bool) error {
	key := roomID + ":" + threadID

	r.streamMu.Lock()
	streamMsg, exists := r.streamingEvents[key]

	if !exists {
		// Start new streaming message
		now := time.Now()
		streamMsg = &StreamingMessage{
			Content:     "",
			LastUpdated: now,
			CreatedAt:   now,
		}
		r.streamingEvents[key] = streamMsg
	}

	// Append the chunk
	streamMsg.Content += chunk
	currentContent := streamMsg.Content
	eventID := streamMsg.EventID
	lastUpdated := streamMsg.LastUpdated

	// Throttle updates to avoid rate limiting (update at most every 500ms)
	if eventID != "" && time.Since(lastUpdated) < 500*time.Millisecond && !isFinal {
		r.streamMu.Unlock()
		return nil
	}
	r.streamMu.Unlock()

	// Build display content with cursor indicator
	displayContent := currentContent
	if !isFinal {
		displayContent += "▌"
	}

	if eventID == "" {
		// Send initial message with MSC4357 live flag
		newEventID, err := r.client.SendLiveMessage(ctx, roomID, threadID, displayContent)
		if err != nil {
			log.Printf("Failed to send initial streaming message: %v", err)
			// Clean up on error to avoid orphaned state
			r.streamMu.Lock()
			delete(r.streamingEvents, key)
			r.streamMu.Unlock()
			return err
		}

		r.streamMu.Lock()
		if sm, ok := r.streamingEvents[key]; ok {
			sm.EventID = newEventID
			sm.LastUpdated = time.Now()
		}
		r.streamMu.Unlock()
	} else {
		// Edit existing message
		err := r.client.EditMessage(ctx, roomID, eventID, displayContent, !isFinal)
		if err != nil {
			log.Printf("Failed to edit streaming message: %v", err)
			// Don't delete on edit failure - message still exists, just update failed
		}

		r.streamMu.Lock()
		if sm, ok := r.streamingEvents[key]; ok {
			sm.LastUpdated = time.Now()
		}
		r.streamMu.Unlock()
	}

	// Clean up if final (do this inside lock to prevent race)
	if isFinal {
		r.streamMu.Lock()
		delete(r.streamingEvents, key)
		r.streamMu.Unlock()
	}

	return nil
}

// handlePermissionRequest forwards a permission request to Matrix
func (r *Router) handlePermissionRequest(ctx context.Context, sessionID string, req *ipc.PermissionRequestPayload) error {
	// Format permission prompt
	var sb strings.Builder
	sb.WriteString("🔐 **Permission Request**\n\n")
	sb.WriteString(fmt.Sprintf("**Tool:** `%s`\n", req.ToolName))
	sb.WriteString(fmt.Sprintf("**Action:** %s\n", req.Description))
	if req.InputPreview != "" {
		sb.WriteString(fmt.Sprintf("**Details:** ```\n%s\n```\n", req.InputPreview))
	}
	sb.WriteString(fmt.Sprintf("\n**ID:** `%s`\n\n", req.RequestID))
	sb.WriteString("Reply with `!allow` or `!deny` (or `!a`/`!d`) to respond.\n")
	sb.WriteString("_Request will timeout in 60 seconds._")

	// Send to room
	_, err := r.client.SendMessage(ctx, req.RoomID, sb.String())
	if err != nil {
		return err
	}

	// Track pending permission
	resultChan := make(chan bool, 1)
	r.permMu.Lock()
	r.pendingPerm[req.RequestID] = &PendingPermission{
		SessionID:  sessionID,
		RequestID:  req.RequestID,
		RoomID:     req.RoomID,
		ResultChan: resultChan,
		CreatedAt:  time.Now(),
	}
	r.permMu.Unlock()

	// Wait for response with timeout
	go func() {
		timeout := 60 * time.Second
		select {
		case allowed := <-resultChan:
			// Check if permission was already cleaned up (e.g., by timeout)
			r.permMu.Lock()
			_, stillPending := r.pendingPerm[req.RequestID]
			if stillPending {
				delete(r.pendingPerm, req.RequestID)
			}
			r.permMu.Unlock()

			if stillPending {
				r.sendPermissionVerdict(sessionID, req.RequestID, allowed)
			}
		case <-time.After(timeout):
			// Atomically check and remove to prevent double-verdict
			r.permMu.Lock()
			_, stillPending := r.pendingPerm[req.RequestID]
			if stillPending {
				delete(r.pendingPerm, req.RequestID)
			}
			r.permMu.Unlock()

			if stillPending {
				r.client.SendMessage(ctx, req.RoomID, fmt.Sprintf("⏰ Permission request `%s` timed out (denied)", req.RequestID))
				r.sendPermissionVerdict(sessionID, req.RequestID, false)
			}
		}
	}()

	return nil
}

// sendPermissionVerdict sends the verdict back to the bridge
func (r *Router) sendPermissionVerdict(sessionID, requestID string, allowed bool) {
	msg, err := ipc.NewIPCMessage(ipc.TypePermissionVerdict, sessionID, &ipc.PermissionVerdictPayload{
		RequestID: requestID,
		Allowed:   allowed,
	})
	if err != nil {
		log.Printf("Failed to create verdict message: %v", err)
		return
	}

	if err := r.ipcServer.SendToSession(sessionID, msg); err != nil {
		log.Printf("Failed to send verdict to session %s: %v", sessionID, err)
	}
}

// handlePermissionResponse processes user's permission response
func (r *Router) handlePermissionResponse(ctx context.Context, roomID, threadID, eventID, requestID string, allowed bool) error {
	r.permMu.Lock()
	pending, exists := r.pendingPerm[requestID]
	if exists {
		delete(r.pendingPerm, requestID)
	}
	r.permMu.Unlock()

	if !exists {
		r.client.SendReply(ctx, roomID, threadID, eventID,
			fmt.Sprintf("No pending permission request found for `%s`", requestID), true)
		return nil
	}

	// Send result to waiting goroutine
	select {
	case pending.ResultChan <- allowed:
	default:
	}

	action := "denied"
	if allowed {
		action = "allowed"
	}
	r.client.SendReply(ctx, roomID, threadID, eventID,
		fmt.Sprintf("Permission `%s` %s", requestID, action), false)

	return nil
}

// cleanupPendingPermission removes a pending permission
func (r *Router) cleanupPendingPermission(requestID string) {
	r.permMu.Lock()
	defer r.permMu.Unlock()
	delete(r.pendingPerm, requestID)
}

// checkPermissionCommand checks if a message is a permission response
func (r *Router) checkPermissionCommand(body string) (bool, string, bool) {
	body = strings.TrimSpace(body)
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return false, "", false
	}

	cmd := strings.ToLower(parts[0])

	if cmd == "!allow" || cmd == "!a" {
		if len(parts) >= 2 {
			return true, parts[1], true
		}
		if id := r.getMostRecentPendingID(); id != "" {
			return true, id, true
		}
		return false, "", false
	}

	if cmd == "!deny" || cmd == "!d" {
		if len(parts) >= 2 {
			return true, parts[1], false
		}
		if id := r.getMostRecentPendingID(); id != "" {
			return true, id, false
		}
		return false, "", false
	}

	return false, "", false
}

// getMostRecentPendingID returns the most recent pending permission ID
func (r *Router) getMostRecentPendingID() string {
	r.permMu.Lock()
	defer r.permMu.Unlock()

	var mostRecent *PendingPermission
	for _, p := range r.pendingPerm {
		if mostRecent == nil || p.CreatedAt.After(mostRecent.CreatedAt) {
			mostRecent = p
		}
	}
	if mostRecent != nil {
		return mostRecent.RequestID
	}
	return ""
}

// handleCommand processes bot commands
func (r *Router) handleCommand(ctx context.Context, roomID, threadID, eventID, body string) error {
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return nil
	}

	cmd := strings.ToLower(parts[0])
	var response string

	switch cmd {
	case "!help":
		response = `**Available Commands:**
- ` + "`!help`" + ` - Show this help
- ` + "`!sessions`" + ` - List active sessions
- ` + "`!status`" + ` - Show current session status
- ` + "`!new`" + ` - Start a new session
- ` + "`!stop`" + ` - Stop current session
- ` + "`!allow [id]`" + ` / ` + "`!a`" + ` - Allow permission request
- ` + "`!deny [id]`" + ` / ` + "`!d`" + ` - Deny permission request`

	case "!sessions":
		sessions := r.spawner.ListSessions()
		if len(sessions) == 0 {
			response = "No active sessions"
		} else {
			var sb strings.Builder
			sb.WriteString("**Active Sessions:**\n")
			for _, s := range sessions {
				sb.WriteString(fmt.Sprintf("- `%s` (%s) - %d messages\n", s.SessionID, s.Status, s.MessageCount))
			}
			response = sb.String()
		}

	case "!status":
		session, exists := r.spawner.GetSessionByRoom(roomID, threadID)
		if !exists {
			response = "No session for this room/thread"
		} else {
			response = fmt.Sprintf("**Session:** `%s`\n**Status:** %s\n**Messages:** %d\n**Created:** %s",
				session.ID, session.Status, session.MessageCount, session.CreatedAt.Format(time.RFC3339))
		}

	case "!new":
		sessionID := r.spawner.makeSessionID(roomID, threadID)
		r.spawner.StopSession(sessionID)
		response = "Session stopped. Send a message to start a new one."

	case "!stop":
		sessionID := r.spawner.makeSessionID(roomID, threadID)
		if err := r.spawner.StopSession(sessionID); err != nil {
			response = fmt.Sprintf("Error: %v", err)
		} else {
			response = "Session stopped"
		}

	default:
		return nil // Not a command we handle
	}

	_, err := r.client.SendReply(ctx, roomID, threadID, eventID, response, false)
	return err
}

// waitForSession waits for a session to become ready
func (r *Router) waitForSession(ctx context.Context, sessionID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		session, exists := r.spawner.GetSession(sessionID)
		if !exists {
			return fmt.Errorf("session not found")
		}
		if session.Status == "ready" {
			return nil
		}
		if session.Status == "error" {
			return fmt.Errorf("session error")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	return fmt.Errorf("timeout waiting for session")
}

// sendErrorReply sends an error message to Matrix
func (r *Router) sendErrorReply(ctx context.Context, roomID, threadID, eventID, message string) {
	r.client.SendReply(ctx, roomID, threadID, eventID, "⚠️ "+message, true)
}

// CleanupStaleState cleans up stale streaming messages and expired permissions
func (r *Router) CleanupStaleState() {
	now := time.Now()

	// Clean up stale streaming messages (older than 30 minutes)
	r.streamMu.Lock()
	for key, msg := range r.streamingEvents {
		if now.Sub(msg.CreatedAt) > 30*time.Minute {
			log.Printf("Cleaning up stale streaming message: %s", key)
			delete(r.streamingEvents, key)
		}
	}
	r.streamMu.Unlock()

	// Clean up expired permissions (older than 90 seconds - 30s buffer over timeout)
	r.permMu.Lock()
	for id, perm := range r.pendingPerm {
		if now.Sub(perm.CreatedAt) > 90*time.Second {
			log.Printf("Cleaning up expired permission: %s", id)
			delete(r.pendingPerm, id)
		}
	}
	r.permMu.Unlock()
}
