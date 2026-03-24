package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/matrix-claude-code/bot/session"
)

// Handler processes bot commands
type Handler struct {
	sessionMgr *session.Manager
}

// NewHandler creates a new command handler
func NewHandler(sessionMgr *session.Manager) *Handler {
	return &Handler{
		sessionMgr: sessionMgr,
	}
}

// CommandResult represents the result of a command execution
type CommandResult struct {
	Message   string
	IsError   bool
	IsCommand bool // true if the input was a command
}

// Parse checks if the input is a command and executes it
func (h *Handler) Parse(ctx context.Context, input, roomID, threadID string) *CommandResult {
	input = strings.TrimSpace(input)

	// Commands start with !
	if !strings.HasPrefix(input, "!") {
		return &CommandResult{IsCommand: false}
	}

	parts := strings.Fields(input)
	if len(parts) == 0 {
		return &CommandResult{IsCommand: false}
	}

	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "!help":
		return h.handleHelp()
	case "!new", "!newsession":
		return h.handleNewSession(ctx, roomID, threadID)
	case "!model", "!setmodel":
		return h.handleSetModel(ctx, roomID, threadID, args)
	case "!session", "!status":
		return h.handleSessionStatus(ctx, roomID, threadID)
	case "!workdir", "!cd":
		return h.handleSetWorkingDir(ctx, roomID, threadID, args)
	default:
		return &CommandResult{
			Message:   fmt.Sprintf("Unknown command: %s. Use !help for available commands.", cmd),
			IsError:   true,
			IsCommand: true,
		}
	}
}

func (h *Handler) handleHelp() *CommandResult {
	help := `**Claude Code Bot Commands**

**Session Management:**
- !new / !newsession - Start a new Claude Code session
- !session / !status - Show current session info
- !workdir <path> / !cd <path> - Set working directory

**Model:**
- !model <name> / !setmodel <name> - Switch to a different model

**Help:**
- !help - Show this help message

**Usage:**
Simply send a message (without a command prefix) to chat with Claude Code.
Each thread maintains its own session context. Use !new to start fresh.

**Available Models:**
- sonnet (default, fast) - alias for latest Sonnet
- opus (most capable) - alias for latest Opus
- haiku (fastest) - alias for latest Haiku

You can also use full model IDs like claude-sonnet-4-6-20250514`

	return &CommandResult{
		Message:   help,
		IsCommand: true,
	}
}

func (h *Handler) handleNewSession(ctx context.Context, roomID, threadID string) *CommandResult {
	session := h.sessionMgr.CreateNewSession(roomID, threadID)

	return &CommandResult{
		Message: fmt.Sprintf("Created new Claude Code session\nModel: %s\nWorking directory: %s",
			valueOrDefault(session.Model, h.sessionMgr.GetDefaultModel(), "(default)"),
			valueOrDefault(session.WorkingDirectory, h.sessionMgr.GetDefaultWorkingDirectory(), ".")),
		IsCommand: true,
	}
}

func (h *Handler) handleSetModel(ctx context.Context, roomID, threadID string, args []string) *CommandResult {
	if len(args) == 0 {
		return &CommandResult{
			Message: `Usage: !model <model_name>

Available models (aliases):
- sonnet (recommended, default)
- opus (most capable)
- haiku (fastest)

Or use full model IDs like claude-sonnet-4-6-20250514`,
			IsError:   true,
			IsCommand: true,
		}
	}

	modelName := args[0]

	// Normalize to Claude Code aliases (Claude Code resolves these to latest versions)
	switch strings.ToLower(modelName) {
	case "sonnet", "claude-sonnet", "sonnet-4", "sonnet-4.6":
		modelName = "sonnet"
	case "opus", "claude-opus", "opus-4", "opus-4.6":
		modelName = "opus"
	case "haiku", "claude-haiku", "haiku-4", "haiku-4.5":
		modelName = "haiku"
	}
	// Otherwise pass through as-is (allows full model IDs like claude-sonnet-4-6-20250514)

	// Ensure session exists
	h.sessionMgr.GetOrCreateSession(roomID, threadID)
	h.sessionMgr.SetModel(roomID, threadID, modelName)

	return &CommandResult{
		Message:   fmt.Sprintf("Model set to: %s", modelName),
		IsCommand: true,
	}
}

func (h *Handler) handleSetWorkingDir(ctx context.Context, roomID, threadID string, args []string) *CommandResult {
	if len(args) == 0 {
		return &CommandResult{
			Message:   "Usage: !workdir <path>\nExample: !workdir /home/user/project",
			IsError:   true,
			IsCommand: true,
		}
	}

	workDir := args[0]

	// Ensure session exists
	h.sessionMgr.GetOrCreateSession(roomID, threadID)
	h.sessionMgr.SetWorkingDirectory(roomID, threadID, workDir)

	return &CommandResult{
		Message:   fmt.Sprintf("Working directory set to: %s", workDir),
		IsCommand: true,
	}
}

func (h *Handler) handleSessionStatus(ctx context.Context, roomID, threadID string) *CommandResult {
	session, exists := h.sessionMgr.GetSession(roomID, threadID)
	if !exists {
		return &CommandResult{
			Message:   "No active session. Send a message to start one, or use !new.",
			IsCommand: true,
		}
	}

	var sb strings.Builder
	sb.WriteString("**Current Session:**\n")
	sb.WriteString(fmt.Sprintf("- Room: `%s`\n", session.RoomID))
	if session.ThreadID != "" {
		sb.WriteString(fmt.Sprintf("- Thread: `%s`\n", session.ThreadID))
	} else {
		sb.WriteString("- Thread: (main room)\n")
	}
	sb.WriteString(fmt.Sprintf("- Model: %s\n", valueOrDefault(session.Model, h.sessionMgr.GetDefaultModel(), "(default)")))
	sb.WriteString(fmt.Sprintf("- Working directory: %s\n", valueOrDefault(session.WorkingDirectory, h.sessionMgr.GetDefaultWorkingDirectory(), ".")))

	return &CommandResult{
		Message:   sb.String(),
		IsCommand: true,
	}
}

func valueOrDefault(value, defaultValue, suffix string) string {
	if value != "" {
		return value
	}
	if defaultValue != "" {
		return defaultValue + suffix
	}
	return "(not set)"
}
