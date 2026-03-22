package appservice

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
)

// Event represents a Matrix event pushed by the homeserver
type Event struct {
	EventID        string                 `json:"event_id"`
	RoomID         string                 `json:"room_id"`
	Sender         string                 `json:"sender"`
	Type           string                 `json:"type"`
	StateKey       *string                `json:"state_key,omitempty"`
	Content        map[string]interface{} `json:"content"`
	OriginServerTS int64                  `json:"origin_server_ts"`
	Unsigned       map[string]interface{} `json:"unsigned,omitempty"`
}

// Transaction represents a batch of events from the homeserver
type Transaction struct {
	Events    []Event `json:"events"`
	Ephemeral []Event `json:"ephemeral,omitempty"`
}

// EventHandler is called for each received event
type EventHandler func(ctx context.Context, event *Event)

// Server implements the Application Service HTTP API
type Server struct {
	hsToken   string
	asToken   string
	botUserID string
	handler   EventHandler
	server    *http.Server

	// Track processed transactions for idempotency
	txnMu        sync.Mutex
	processedTxn map[string]bool
}

// NewServer creates a new Application Service server
func NewServer(hsToken, asToken, botUserID string, handler EventHandler) *Server {
	return &Server{
		hsToken:      hsToken,
		asToken:      asToken,
		botUserID:    botUserID,
		handler:      handler,
		processedTxn: make(map[string]bool),
	}
}

// Start starts the AS HTTP server
func (s *Server) Start(ctx context.Context, addr string) error {
	mux := http.NewServeMux()

	// Application Service API endpoints
	mux.HandleFunc("PUT /_matrix/app/v1/transactions/{txnId}", s.handleTransaction)
	mux.HandleFunc("GET /_matrix/app/v1/users/{userId}", s.handleUserQuery)
	mux.HandleFunc("GET /_matrix/app/v1/rooms/{roomAlias}", s.handleRoomQuery)
	mux.HandleFunc("POST /_matrix/app/v1/ping", s.handlePing)

	// Legacy endpoints (fallback)
	mux.HandleFunc("PUT /transactions/{txnId}", s.handleTransaction)
	mux.HandleFunc("GET /users/{userId}", s.handleUserQuery)
	mux.HandleFunc("GET /rooms/{roomAlias}", s.handleRoomQuery)

	// Health check
	mux.HandleFunc("GET /health", s.handleHealth)

	s.server = &http.Server{
		Addr:    addr,
		Handler: s.authMiddleware(mux),
	}

	log.Printf("Application Service server starting on %s", addr)

	errChan := make(chan error, 1)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return s.server.Shutdown(context.Background())
	}
}

// Stop gracefully stops the server
func (s *Server) Stop(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// authMiddleware verifies the hs_token from the homeserver
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health endpoint doesn't require auth
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" {
			// Also check query parameter for legacy support
			auth = "Bearer " + r.URL.Query().Get("access_token")
		}

		expectedAuth := "Bearer " + s.hsToken
		if auth != expectedAuth {
			s.sendError(w, http.StatusForbidden, "M_FORBIDDEN", "Invalid hs_token")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleTransaction receives events from the homeserver
func (s *Server) handleTransaction(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received transaction request: %s %s", r.Method, r.URL.Path)
	txnID := r.PathValue("txnId")
	if txnID == "" {
		// Try legacy path parsing
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) > 0 {
			txnID = parts[len(parts)-1]
		}
	}

	// Check if we've already processed this transaction (idempotency)
	s.txnMu.Lock()
	if s.processedTxn[txnID] {
		s.txnMu.Unlock()
		// Already processed, return success
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
		return
	}
	s.txnMu.Unlock()

	var txn Transaction
	if err := json.NewDecoder(r.Body).Decode(&txn); err != nil {
		log.Printf("Failed to decode transaction: %v", err)
		s.sendError(w, http.StatusBadRequest, "M_BAD_JSON", "Invalid JSON")
		return
	}

	log.Printf("Transaction %s contains %d events", txnID, len(txn.Events))

	// Process events
	ctx := r.Context()
	for i := range txn.Events {
		event := &txn.Events[i]

		// Skip events from the bot itself
		if event.Sender == s.botUserID {
			continue
		}

		if s.handler != nil {
			s.handler(ctx, event)
		}
	}

	// Mark transaction as processed
	s.txnMu.Lock()
	s.processedTxn[txnID] = true
	// Cleanup old transactions (keep last 1000)
	if len(s.processedTxn) > 1000 {
		count := 0
		for k := range s.processedTxn {
			if count > 500 {
				break
			}
			delete(s.processedTxn, k)
			count++
		}
	}
	s.txnMu.Unlock()

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

// handleUserQuery responds to user existence queries
func (s *Server) handleUserQuery(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")
	if userID == "" {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) > 0 {
			userID = parts[len(parts)-1]
		}
	}

	// Only claim our bot user exists
	if userID == s.botUserID {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
		return
	}

	s.sendError(w, http.StatusNotFound, "M_NOT_FOUND", "User not found")
}

// handleRoomQuery responds to room alias queries
func (s *Server) handleRoomQuery(w http.ResponseWriter, r *http.Request) {
	// We don't manage any room aliases
	s.sendError(w, http.StatusNotFound, "M_NOT_FOUND", "Room alias not found")
}

// handlePing responds to connectivity tests
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

// handleHealth returns health status
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"healthy": true,
	})
}

// sendError sends a Matrix-formatted error response
func (s *Server) sendError(w http.ResponseWriter, status int, errcode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"errcode": errcode,
		"error":   message,
	})
}

// GetASToken returns the AS token for making API calls
func (s *Server) GetASToken() string {
	return s.asToken
}
