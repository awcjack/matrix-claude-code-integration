// Bridge is the child MCP server that runs with each Claude Code instance
package main

import (
	"log"
	"os"

	"github.com/anthropics/matrix-claude-code/appservice/bridge"
)

func main() {
	// Get configuration from environment (set by coordinator)
	sessionID := os.Getenv("BRIDGE_SESSION_ID")
	socketPath := os.Getenv("BRIDGE_IPC_SOCKET")
	roomID := os.Getenv("BRIDGE_ROOM_ID")
	threadID := os.Getenv("BRIDGE_THREAD_ID")

	if sessionID == "" {
		log.Fatal("BRIDGE_SESSION_ID environment variable is required")
	}
	if socketPath == "" {
		log.Fatal("BRIDGE_IPC_SOCKET environment variable is required")
	}
	if roomID == "" {
		log.Fatal("BRIDGE_ROOM_ID environment variable is required")
	}

	log.Printf("Bridge starting: session=%s socket=%s room=%s thread=%s",
		sessionID, socketPath, roomID, threadID)

	// Create and run bridge
	b := bridge.NewBridge(sessionID, socketPath, roomID, threadID)

	if err := b.Run(); err != nil {
		log.Fatalf("Bridge error: %v", err)
	}

	log.Println("Bridge stopped")
}
