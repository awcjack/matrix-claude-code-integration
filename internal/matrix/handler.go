package matrix

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/personal/matrix-claude-code-integration/internal/appservice"
	"github.com/personal/matrix-claude-code-integration/internal/channel"
	"github.com/personal/matrix-claude-code-integration/internal/commands"
	"github.com/personal/matrix-claude-code-integration/internal/config"
	"github.com/personal/matrix-claude-code-integration/internal/session"
)

// MatrixClient is an interface for sending messages to Matrix
type MatrixClient interface {
	SendMessage(ctx context.Context, roomID, message string) (string, error)
	SendLiveMessage(ctx context.Context, roomID, threadID, message string) (string, error)
	EditMessage(ctx context.Context, roomID, eventID, newContent string, isLive bool) error
	SendReply(ctx context.Context, roomID, threadID, replyTo, message string, isNotice bool) (string, error)
	SetTyping(ctx context.Context, roomID string, typing bool, timeoutMS int) error
	JoinRoom(ctx context.Context, roomID string) error
	GetBotUserID() string
}

// Handler handles Matrix events and coordinates with Claude Code via MCP channel
type Handler struct {
	client     MatrixClient
	mcpServer  *channel.MCPServer
	sessionMgr *session.Manager
	cmdHandler *commands.Handler
	cfg        *config.Config

	// Track messages being streamed
	streamingMu   sync.Mutex
	streamingMsgs map[string]*StreamingMessage // sessionKey -> streaming message

	// Track the handler start time to ignore old events
	startTime time.Time
}

// StreamingMessage tracks a message being streamed
type StreamingMessage struct {
	RoomID   string
	ThreadID string
	EventID  string // The message event we're editing
	Content  strings.Builder
	LastEdit time.Time
	IsLive   bool // Whether the message is still streaming (MSC4357)
}

// NewHandler creates a new event handler with MCP channel
func NewHandler(client MatrixClient, mcpServer *channel.MCPServer, cfg *config.Config) *Handler {
	sessionMgr := session.NewManager(
		cfg.ClaudeCode.WorkingDirectory,
		cfg.ClaudeCode.Model,
		cfg.ClaudeCode.SystemPrompt,
	)

	return &Handler{
		client:        client,
		mcpServer:     mcpServer,
		sessionMgr:    sessionMgr,
		cmdHandler:    commands.NewHandler(sessionMgr),
		cfg:           cfg,
		streamingMsgs: make(map[string]*StreamingMessage),
		startTime:     time.Now(),
	}
}

// HandleEvent processes a Matrix event (from either AS or bot mode)
func (h *Handler) HandleEvent(ctx context.Context, event *appservice.Event) {
	log.Printf("HandleEvent: type=%s sender=%s room=%s", event.Type, event.Sender, event.RoomID)

	// Only handle m.room.message events
	if event.Type != "m.room.message" {
		// Handle invites
		if event.Type == "m.room.member" {
			h.handleMemberEvent(ctx, event)
		}
		return
	}

	// Ignore events from before handler started
	if event.OriginServerTS < h.startTime.UnixMilli() {
		log.Printf("Ignoring old event (ts=%d < start=%d): %s", event.OriginServerTS, h.startTime.UnixMilli(), event.EventID)
		return
	}

	// Ignore messages from the bot itself
	if event.Sender == h.client.GetBotUserID() {
		return
	}

	// Check whitelist
	if !h.cfg.IsUserWhitelisted(event.Sender) {
		log.Printf("Ignoring message from non-whitelisted user: %s", event.Sender)
		return
	}

	// Extract message content
	msgType, _ := event.Content["msgtype"].(string)
	if msgType != "m.text" {
		return
	}

	body, _ := event.Content["body"].(string)
	if body == "" {
		return
	}

	// Extract thread info
	threadID := ""
	if relatesTo, ok := event.Content["m.relates_to"].(map[string]interface{}); ok {
		if relType, _ := relatesTo["rel_type"].(string); relType == "m.thread" {
			threadID, _ = relatesTo["event_id"].(string)
		}
	}

	roomID := event.RoomID
	eventID := event.EventID

	log.Printf("Received message from %s in %s (thread: %s): %s",
		event.Sender, roomID, threadID, truncate(body, 50))

	// Check if it's a command
	result := h.cmdHandler.Parse(ctx, body, roomID, threadID)
	if result.IsCommand {
		h.sendReply(ctx, roomID, threadID, eventID, result.Message, result.IsError)
		return
	}

	// It's a regular message - push to Claude Code via MCP channel
	h.handleClaudeCodeMessage(ctx, roomID, threadID, eventID, event.Sender, body)
}

// handleMemberEvent handles membership events (invites)
func (h *Handler) handleMemberEvent(ctx context.Context, event *appservice.Event) {
	membership, _ := event.Content["membership"].(string)
	stateKey := ""
	if event.StateKey != nil {
		stateKey = *event.StateKey
	}

	// Auto-join rooms we're invited to
	if membership == "invite" && stateKey == h.client.GetBotUserID() {
		log.Printf("Invited to room %s by %s, auto-joining...", event.RoomID, event.Sender)
		if err := h.client.JoinRoom(ctx, event.RoomID); err != nil {
			log.Printf("Failed to join room %s: %v", event.RoomID, err)
		} else {
			log.Printf("Successfully joined room %s", event.RoomID)
		}
	}
}

// handleClaudeCodeMessage sends a message to Claude Code and handles the response
func (h *Handler) handleClaudeCodeMessage(ctx context.Context, roomID, threadID, eventID, sender, message string) {
	// Get or create session
	sess := h.sessionMgr.GetOrCreateSession(roomID, threadID)
	_ = sess // Session info available for future use

	// Send typing indicator
	h.client.SetTyping(ctx, roomID, true, 30000)

	// Push the message to Claude Code via MCP channel notification
	if h.mcpServer == nil {
		log.Printf("MCP server not configured")
		h.sendReply(ctx, roomID, threadID, eventID,
			"Claude Code is not configured", true)
		h.client.SetTyping(ctx, roomID, false, 0)
		return
	}

	if err := h.mcpServer.PushMessage(roomID, threadID, sender, eventID, message); err != nil {
		log.Printf("Failed to push message to Claude Code: %v", err)
		h.sendReply(ctx, roomID, threadID, eventID,
			"Failed to send message to Claude Code: "+err.Error(), true)
		h.client.SetTyping(ctx, roomID, false, 0)
		return
	}

	log.Printf("Message pushed to Claude Code channel: room=%s thread=%s", roomID, threadID)
	// Note: The reply will come back through the MCP channel's reply tool
	// which calls HandleReply
}

// HandleReply handles a reply from Claude Code coming through the MCP channel
func (h *Handler) HandleReply(ctx context.Context, roomID, threadID, message string) error {
	log.Printf("Sending reply to Matrix: room=%s thread=%s len=%d", roomID, threadID, len(message))

	// Stop typing indicator
	h.client.SetTyping(ctx, roomID, false, 0)

	// Determine if we should use thread or main room
	var eventID string
	var err error

	if threadID != "" {
		// Reply in thread
		eventID, err = h.client.SendReply(ctx, roomID, threadID, threadID, message, false)
	} else {
		// Send to main room
		eventID, err = h.client.SendMessage(ctx, roomID, message)
	}

	if err != nil {
		log.Printf("Failed to send reply to Matrix: %v", err)
		return err
	}

	log.Printf("Reply sent to Matrix: event_id=%s", eventID)
	return nil
}

// HandleStreamingReply handles streaming reply chunks from Claude Code
func (h *Handler) HandleStreamingReply(ctx context.Context, roomID, threadID, chunk string, isFinal bool) error {
	sessionKey := roomID + ":" + threadID

	h.streamingMu.Lock()
	streamMsg, exists := h.streamingMsgs[sessionKey]

	if !exists {
		// Start new streaming message
		streamMsg = &StreamingMessage{
			RoomID:   roomID,
			ThreadID: threadID,
			IsLive:   true,
		}
		h.streamingMsgs[sessionKey] = streamMsg
	}

	streamMsg.Content.WriteString(chunk)
	currentContent := streamMsg.Content.String()
	eventID := streamMsg.EventID
	lastEdit := streamMsg.LastEdit
	h.streamingMu.Unlock()

	// Throttle updates to avoid rate limiting
	if time.Since(lastEdit) < 500*time.Millisecond && eventID != "" && !isFinal {
		return nil
	}

	if eventID == "" {
		// Send initial message with MSC4357 live flag
		displayContent := currentContent
		if !isFinal {
			displayContent += "▌"
		}

		newEventID, err := h.client.SendLiveMessage(ctx, roomID, threadID, displayContent)
		if err != nil {
			log.Printf("Failed to send initial streaming message: %v", err)
			return err
		}

		h.streamingMu.Lock()
		if sm, ok := h.streamingMsgs[sessionKey]; ok {
			sm.EventID = newEventID
			sm.LastEdit = time.Now()
		}
		h.streamingMu.Unlock()
	} else {
		// Edit existing message
		displayContent := currentContent
		if !isFinal {
			displayContent += "▌"
		}

		err := h.client.EditMessage(ctx, roomID, eventID, displayContent, !isFinal)
		if err != nil {
			log.Printf("Failed to edit streaming message: %v", err)
		}

		h.streamingMu.Lock()
		if sm, ok := h.streamingMsgs[sessionKey]; ok {
			sm.LastEdit = time.Now()
		}
		h.streamingMu.Unlock()
	}

	// Clean up if final
	if isFinal {
		h.client.SetTyping(ctx, roomID, false, 0)

		h.streamingMu.Lock()
		delete(h.streamingMsgs, sessionKey)
		h.streamingMu.Unlock()
	}

	return nil
}

// sendReply sends a reply to a message
func (h *Handler) sendReply(ctx context.Context, roomID, threadID, replyTo, message string, isError bool) {
	_, err := h.client.SendReply(ctx, roomID, threadID, replyTo, message, isError)
	if err != nil {
		log.Printf("Failed to send reply: %v", err)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
