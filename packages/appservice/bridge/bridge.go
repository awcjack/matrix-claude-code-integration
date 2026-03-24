package bridge

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/anthropics/matrix-claude-code/appservice/ipc"
)

// Bridge connects the MCP server to the coordinator via IPC
type Bridge struct {
	sessionID string
	roomID    string
	threadID  string

	mcpServer *MCPServer
	ipcClient *ipc.Client

	ctx    context.Context
	cancel context.CancelFunc
}

// NewBridge creates a new bridge instance
func NewBridge(sessionID, socketPath, roomID, threadID string) *Bridge {
	ctx, cancel := context.WithCancel(context.Background())

	b := &Bridge{
		sessionID: sessionID,
		roomID:    roomID,
		threadID:  threadID,
		mcpServer: NewMCPServer(sessionID, roomID, threadID),
		ipcClient: ipc.NewClient(socketPath, sessionID),
		ctx:       ctx,
		cancel:    cancel,
	}

	// Set up MCP handlers to forward to IPC
	b.mcpServer.SetReplyHandler(b.handleReply)
	b.mcpServer.SetStreamingReplyHandler(b.handleStreamingReply)
	b.mcpServer.SetPermissionHandler(b.handlePermissionRequest)

	// Set up IPC handlers
	b.ipcClient.SetMessageHandler(b.handleIPCMessage)
	b.ipcClient.SetPermissionVerdictHandler(b.handlePermissionVerdict)
	b.ipcClient.SetShutdownHandler(b.handleShutdown)

	return b
}

// Run starts the bridge
func (b *Bridge) Run() error {
	log.Printf("Bridge starting: session=%s room=%s", b.sessionID, b.roomID)

	// Connect to coordinator
	if err := b.ipcClient.Connect(b.ctx); err != nil {
		return err
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Bridge received shutdown signal")
		b.cancel()
	}()

	// Run IPC client in background
	go func() {
		if err := b.ipcClient.Run(b.ctx); err != nil {
			log.Printf("IPC client error: %v", err)
			b.cancel()
		}
	}()

	// Run MCP server (blocks until done)
	err := b.mcpServer.Run(b.ctx)

	// Cleanup
	b.ipcClient.Close()

	return err
}

// handleReply forwards Claude's reply to the coordinator
func (b *Bridge) handleReply(roomID, threadID, text string) error {
	return b.ipcClient.SendReply(roomID, threadID, text)
}

// handleStreamingReply forwards streaming reply chunks to the coordinator
func (b *Bridge) handleStreamingReply(roomID, threadID, chunk string, isFinal bool) error {
	return b.ipcClient.SendStreamingReply(roomID, threadID, chunk, isFinal)
}

// handlePermissionRequest forwards permission requests to the coordinator
func (b *Bridge) handlePermissionRequest(requestID, toolName, description, inputPreview string) error {
	return b.ipcClient.SendPermissionRequest(requestID, toolName, description, inputPreview, b.roomID)
}

// handleIPCMessage processes messages from the coordinator
func (b *Bridge) handleIPCMessage(msg *ipc.IPCMessage) error {
	if msg.Type != ipc.TypeMatrixMessage {
		return nil
	}

	var payload ipc.MatrixMessagePayload
	if err := msg.ParsePayload(&payload); err != nil {
		return err
	}

	// Forward to Claude via MCP
	return b.mcpServer.PushMessage(
		payload.RoomID,
		payload.ThreadID,
		payload.Sender,
		payload.EventID,
		payload.Body,
	)
}

// handlePermissionVerdict forwards permission verdicts to Claude
func (b *Bridge) handlePermissionVerdict(requestID string, allowed bool) error {
	b.mcpServer.SendPermissionVerdict(requestID, allowed)
	return nil
}

// handleShutdown handles shutdown requests from the coordinator
func (b *Bridge) handleShutdown(reason string, timeout int) error {
	log.Printf("Bridge shutdown requested: %s (timeout: %ds)", reason, timeout)
	b.cancel()
	return nil
}
