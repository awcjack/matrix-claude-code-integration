// Bridge is the child MCP server that runs with each Claude Code instance
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"time"

	"github.com/anthropics/matrix-claude-code/appservice/bridge"
)

func main() {
	// Parse command-line flags (preferred) or fallback to environment variables
	// Command-line args are used when spawned by Claude Code via --channels
	// Environment variables are used when run directly by coordinator (legacy/testing)
	sessionIDFlag := flag.String("session-id", "", "Session ID")
	socketPathFlag := flag.String("socket", "", "IPC socket path")
	roomIDFlag := flag.String("room-id", "", "Matrix room ID")
	threadIDFlag := flag.String("thread-id", "", "Matrix thread ID (optional)")
	flag.Parse()

	// Use flags if provided, otherwise fall back to environment variables
	sessionID := *sessionIDFlag
	if sessionID == "" {
		sessionID = os.Getenv("BRIDGE_SESSION_ID")
	}
	socketPath := *socketPathFlag
	if socketPath == "" {
		socketPath = os.Getenv("BRIDGE_IPC_SOCKET")
	}
	roomID := *roomIDFlag
	if roomID == "" {
		roomID = os.Getenv("BRIDGE_ROOM_ID")
	}
	threadID := *threadIDFlag
	if threadID == "" {
		threadID = os.Getenv("BRIDGE_THREAD_ID")
	}

	if sessionID == "" {
		log.Fatal("session-id flag or BRIDGE_SESSION_ID environment variable is required")
	}
	if socketPath == "" {
		log.Fatal("socket flag or BRIDGE_IPC_SOCKET environment variable is required")
	}
	if roomID == "" {
		log.Fatal("room-id flag or BRIDGE_ROOM_ID environment variable is required")
	}

	// Log to stderr so Claude Code can capture it
	// Also log to a file for debugging
	logFile, err := os.OpenFile("/tmp/matrix-bridge-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}

	log.Printf("===== Bridge binary started =====")
	log.Printf("Bridge starting: session=%s socket=%s room=%s thread=%s",
		sessionID, socketPath, roomID, threadID)
	log.Printf("Working directory: %s", func() string { d, _ := os.Getwd(); return d }())
	log.Printf("PID: %d", os.Getpid())
	log.Printf("Args: %v", os.Args)

	// Wait for socket to be available with timeout
	log.Printf("Waiting for socket at: %s", socketPath)
	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		log.Fatalf("Socket not available: %v", err)
	}
	log.Printf("Socket is available")

	// Create and run bridge
	log.Printf("Creating bridge instance...")
	b := bridge.NewBridge(sessionID, socketPath, roomID, threadID)

	log.Printf("Starting bridge Run()...")
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
