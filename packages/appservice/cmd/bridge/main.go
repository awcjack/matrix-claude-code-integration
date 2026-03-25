// Bridge is the child MCP server that runs with each Claude Code instance
package main

import (
	"log"
	"net"
	"os"
	"time"

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

	// Wait for socket to be available with timeout
	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		log.Fatalf("Socket not available: %v", err)
	}

	// Create and run bridge
	b := bridge.NewBridge(sessionID, socketPath, roomID, threadID)

	if err := b.Run(); err != nil {
		log.Fatalf("Bridge error: %v", err)
	}

	log.Println("Bridge stopped")
}

// waitForSocket waits for the Unix socket to become available
func waitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Check if socket file exists
		if _, err := os.Stat(socketPath); err == nil {
			// Try to connect to verify socket is accepting connections
			conn, err := net.DialTimeout("unix", socketPath, 1*time.Second)
			if err == nil {
				conn.Close()
				return nil
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	return &socketTimeoutError{path: socketPath, timeout: timeout}
}

type socketTimeoutError struct {
	path    string
	timeout time.Duration
}

func (e *socketTimeoutError) Error() string {
	return "timeout waiting for socket " + e.path + " after " + e.timeout.String()
}
