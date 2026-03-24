package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/matrix-claude-code/appservice/ipc"
	"github.com/anthropics/matrix-claude-code/appservice/matrix"
)

// ServerConfig holds configuration for the coordinator server
type ServerConfig struct {
	ListenAddress string
	HSToken       string
	ASToken       string
	BotUserID     string
	BridgePath    string
	SocketPath    string
	Whitelist     []string
	SessionConfig SessionConfig
	IdleTimeout   time.Duration
}

// Server is the main coordinator server
type Server struct {
	config     ServerConfig
	httpServer *http.Server
	ipcServer  *ipc.Server
	spawner    *Spawner
	router     *Router
	client     matrix.MatrixClient

	// Transaction idempotency
	txnMu        sync.Mutex
	processedTxn map[string]bool

	ctx    context.Context
	cancel context.CancelFunc
}

// NewServer creates a new coordinator server
func NewServer(config ServerConfig, client matrix.MatrixClient) *Server {
	ctx, cancel := context.WithCancel(context.Background())

	spawner := NewSpawner(config.BridgePath, config.SocketPath, config.SessionConfig)

	server := &Server{
		config:       config,
		spawner:      spawner,
		client:       client,
		processedTxn: make(map[string]bool),
		ctx:          ctx,
		cancel:       cancel,
	}

	// Create IPC server
	server.ipcServer = ipc.NewServer(config.SocketPath, server.handleIPCMessage)

	// Create router
	server.router = NewRouter(spawner, server.ipcServer, client, config.Whitelist)

	return server
}

// Start starts the coordinator server
func (s *Server) Start() error {
	// Start IPC server
	if err := s.ipcServer.Start(); err != nil {
		return fmt.Errorf("start IPC server: %w", err)
	}

	// Set up HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/_matrix/app/v1/transactions/", s.authMiddleware(s.handleTransactions))
	mux.HandleFunc("/_matrix/app/v1/users/", s.authMiddleware(s.handleUserQuery))
	mux.HandleFunc("/_matrix/app/v1/rooms/", s.authMiddleware(s.handleRoomQuery))

	s.httpServer = &http.Server{
		Addr:    s.config.ListenAddress,
		Handler: mux,
	}

	// Start idle session cleanup
	go s.cleanupLoop()

	log.Printf("Coordinator server starting on %s", s.config.ListenAddress)

	go func() {
		if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return nil
}

// Stop stops the coordinator server
func (s *Server) Stop() error {
	s.cancel()

	// Stop HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.httpServer.Shutdown(ctx)

	// Stop IPC server
	s.ipcServer.Stop()

	// Stop all sessions
	s.spawner.Shutdown()

	return nil
}

// handleIPCMessage processes messages from bridge processes
func (s *Server) handleIPCMessage(sessionID string, msg *ipc.IPCMessage) error {
	return s.router.HandleIPCMessage(sessionID, msg)
}

// authMiddleware validates the Authorization header
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + s.config.HSToken

		if auth != expected {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	sessions := s.spawner.ListSessions()
	connected := s.ipcServer.GetConnectedSessions()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":             "ok",
		"sessions":           len(sessions),
		"connected_sessions": len(connected),
	})
}

// handleTransactions handles incoming Matrix events
func (s *Server) handleTransactions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract transaction ID
	path := r.URL.Path
	parts := strings.Split(path, "/")
	if len(parts) < 5 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	txnID := parts[len(parts)-1]

	// Check idempotency
	s.txnMu.Lock()
	if s.processedTxn[txnID] {
		s.txnMu.Unlock()
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
		return
	}
	s.processedTxn[txnID] = true
	s.txnMu.Unlock()

	// Parse transaction
	var txn struct {
		Events []Event `json:"events"`
	}

	if err := json.NewDecoder(r.Body).Decode(&txn); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Process events
	for _, event := range txn.Events {
		s.processEvent(r.Context(), &event)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

// Event represents a Matrix event
type Event struct {
	EventID        string                 `json:"event_id"`
	RoomID         string                 `json:"room_id"`
	Sender         string                 `json:"sender"`
	Type           string                 `json:"type"`
	Content        map[string]interface{} `json:"content"`
	StateKey       *string                `json:"state_key,omitempty"`
	OriginServerTS int64                  `json:"origin_server_ts"`
}

// processEvent processes a single Matrix event
func (s *Server) processEvent(ctx context.Context, event *Event) {
	// Ignore events from the bot itself
	if event.Sender == s.config.BotUserID {
		return
	}

	// Handle member events (invites)
	if event.Type == "m.room.member" {
		s.handleMemberEvent(ctx, event)
		return
	}

	// Only handle message events
	if event.Type != "m.room.message" {
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

	// Route to session
	if err := s.router.HandleMatrixEvent(ctx, event.RoomID, threadID, event.Sender, event.EventID, body, event.OriginServerTS); err != nil {
		log.Printf("Error handling event: %v", err)
	}
}

// handleMemberEvent handles membership events (invites)
func (s *Server) handleMemberEvent(ctx context.Context, event *Event) {
	membership, _ := event.Content["membership"].(string)
	stateKey := ""
	if event.StateKey != nil {
		stateKey = *event.StateKey
	}

	// Auto-join rooms we're invited to
	if membership == "invite" && stateKey == s.config.BotUserID {
		log.Printf("Invited to room %s by %s, auto-joining...", event.RoomID, event.Sender)
		if err := s.client.JoinRoom(ctx, event.RoomID); err != nil {
			log.Printf("Failed to join room %s: %v", event.RoomID, err)
		} else {
			log.Printf("Successfully joined room %s", event.RoomID)
		}
	}
}

// handleUserQuery handles user existence queries
func (s *Server) handleUserQuery(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

// handleRoomQuery handles room alias queries
func (s *Server) handleRoomQuery(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

// cleanupLoop periodically cleans up idle sessions
func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.spawner.CleanupIdleSessions(s.config.IdleTimeout)
		}
	}
}
