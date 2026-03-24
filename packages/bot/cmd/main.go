package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/anthropics/matrix-claude-code/bot/channel"
	"github.com/anthropics/matrix-claude-code/bot/config"
	"github.com/anthropics/matrix-claude-code/bot/matrix"
)

func main() {
	configPath := flag.String("config", "", "Path to config file (JSON)")
	stdioMode := flag.Bool("stdio", false, "Run as MCP stdio server for Claude Code --channels")
	flag.Parse()

	var cfg *config.Config
	var err error

	if *configPath != "" {
		cfg, err = config.Load(*configPath)
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}
	} else {
		cfg = config.LoadFromEnv()
	}

	// Override stdio mode from flag
	if *stdioMode {
		cfg.Channel.StdioMode = true
	}

	// Validate config
	if cfg.Matrix.Homeserver == "" {
		log.Fatal("Matrix homeserver is required (MATRIX_HOMESERVER)")
	}
	if len(cfg.Whitelist) == 0 {
		log.Fatal("At least one whitelisted user is required (MATRIX_WHITELIST)")
	}
	if cfg.Matrix.UserID == "" {
		log.Fatal("Matrix user ID is required (MATRIX_USER_ID)")
	}
	if cfg.Matrix.AccessToken == "" {
		log.Fatal("Matrix access token is required (MATRIX_ACCESS_TOKEN)")
	}

	log.Println("Starting Matrix-Claude Code integration")
	log.Printf("Matrix homeserver: %s", cfg.Matrix.Homeserver)
	log.Printf("Bot user ID: %s", cfg.Matrix.UserID)
	log.Printf("Channel name: %s", cfg.Channel.Name)
	log.Printf("Whitelisted users: %d", len(cfg.Whitelist))

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal")
		cancel()
	}()

	// Run in appropriate mode
	if cfg.Channel.StdioMode {
		runStdioMode(ctx, cfg)
	} else {
		runBotMode(ctx, cfg)
	}

	log.Println("Integration stopped")
}

// runStdioMode runs as an MCP server for Claude Code --channels
// Claude Code spawns this as a subprocess and communicates via stdin/stdout
func runStdioMode(ctx context.Context, cfg *config.Config) {
	log.Println("Running in MCP stdio mode for Claude Code --channels")

	// Create mautrix client for Matrix communication
	userID := id.UserID(cfg.Matrix.UserID)
	matrixClient, err := mautrix.NewClient(cfg.Matrix.Homeserver, userID, cfg.Matrix.AccessToken)
	if err != nil {
		log.Fatalf("Failed to create Matrix client: %v", err)
	}

	if cfg.Matrix.DeviceID != "" {
		matrixClient.DeviceID = id.DeviceID(cfg.Matrix.DeviceID)
	}

	clientAdapter := matrix.NewBotClientAdapter(matrixClient)

	// Create MCP server with reply handler and permission handler
	var handler *matrix.Handler

	mcpServer := channel.NewMCPServer(
		cfg.Channel.Name,
		"1.0.0",
		func(ctx context.Context, roomID, threadID, message string) error {
			// Send reply to Matrix
			if handler != nil {
				return handler.HandleReply(ctx, roomID, threadID, message)
			}
			return nil
		},
		func(ctx context.Context, req *channel.PermissionRequest) (bool, error) {
			// Handle permission request from Claude Code
			if handler != nil {
				return handler.HandlePermissionRequest(ctx, req)
			}
			return false, nil
		},
	)

	// Create handler
	handler = matrix.NewHandler(clientAdapter, mcpServer, cfg)

	// Set up syncer for Matrix events
	syncer := mautrix.NewDefaultSyncer()

	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		asEvent := convertToMatrixEvent(evt)
		handler.HandleEvent(ctx, asEvent)
	})

	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		asEvent := convertToMatrixEvent(evt)
		handler.HandleEvent(ctx, asEvent)
	})

	matrixClient.Syncer = syncer
	matrixClient.Store = mautrix.NewMemorySyncStore()

	// Start Matrix sync in background
	go func() {
		log.Println("Starting Matrix sync...")
		if err := matrixClient.SyncWithContext(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("Matrix sync error: %v", err)
			}
		}
	}()

	log.Printf("MCP Channel server ready. Listening for Matrix messages...")

	// Run MCP server (blocks until context is cancelled)
	if err := mcpServer.Run(ctx); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("MCP server error: %v", err)
		}
	}
}

// runBotMode runs the integration as a regular Matrix bot (polling only, no MCP)
func runBotMode(ctx context.Context, cfg *config.Config) {
	log.Println("Running in Bot mode (standalone, no MCP)")
	log.Println("Note: Bot mode without --stdio does not integrate with Claude Code")
	log.Println("Use --stdio flag or set CHANNEL_STDIO_MODE=true for Claude Code integration")

	// Create mautrix client
	userID := id.UserID(cfg.Matrix.UserID)
	client, err := mautrix.NewClient(cfg.Matrix.Homeserver, userID, cfg.Matrix.AccessToken)
	if err != nil {
		log.Fatalf("Failed to create Matrix client: %v", err)
	}

	if cfg.Matrix.DeviceID != "" {
		client.DeviceID = id.DeviceID(cfg.Matrix.DeviceID)
	}

	// Create adapter
	clientAdapter := matrix.NewBotClientAdapter(client)

	// Create MCP server with reply handler (even though not connected to Claude)
	var handler *matrix.Handler
	mcpServer := channel.NewMCPServer(
		cfg.Channel.Name,
		"1.0.0",
		func(ctx context.Context, roomID, threadID, message string) error {
			if handler != nil {
				return handler.HandleReply(ctx, roomID, threadID, message)
			}
			return nil
		},
		func(ctx context.Context, req *channel.PermissionRequest) (bool, error) {
			// Handle permission request from Claude Code
			if handler != nil {
				return handler.HandlePermissionRequest(ctx, req)
			}
			return false, nil
		},
	)

	// Create handler
	handler = matrix.NewHandler(clientAdapter, mcpServer, cfg)

	// Set up syncer
	syncer := mautrix.NewDefaultSyncer()

	// Handle message events
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		asEvent := convertToMatrixEvent(evt)
		handler.HandleEvent(ctx, asEvent)
	})

	// Handle member events (for invites)
	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		asEvent := convertToMatrixEvent(evt)
		handler.HandleEvent(ctx, asEvent)
	})

	client.Syncer = syncer
	client.Store = mautrix.NewMemorySyncStore()

	log.Printf("Bot is running. Press Ctrl+C to stop.")

	// Start syncing
	if err := client.SyncWithContext(ctx); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("Sync error: %v", err)
		}
	}
}

// convertToMatrixEvent converts a mautrix event to matrix.Event format
func convertToMatrixEvent(evt *event.Event) *matrix.Event {
	var stateKey *string
	if evt.StateKey != nil {
		sk := *evt.StateKey
		stateKey = &sk
	}

	return &matrix.Event{
		EventID:        string(evt.ID),
		RoomID:         string(evt.RoomID),
		Sender:         string(evt.Sender),
		Type:           evt.Type.Type,
		StateKey:       stateKey,
		Content:        evt.Content.Raw,
		OriginServerTS: evt.Timestamp,
	}
}
