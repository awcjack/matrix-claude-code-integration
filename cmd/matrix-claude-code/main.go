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

	"github.com/personal/matrix-claude-code-integration/internal/appservice"
	"github.com/personal/matrix-claude-code-integration/internal/channel"
	"github.com/personal/matrix-claude-code-integration/internal/config"
	"github.com/personal/matrix-claude-code-integration/internal/ipc"
	"github.com/personal/matrix-claude-code-integration/internal/matrix"
)

func main() {
	configPath := flag.String("config", "", "Path to config file (JSON)")
	generateReg := flag.Bool("generate-registration", false, "Generate AS registration YAML and exit")
	regOutput := flag.String("registration-output", "registration.yaml", "Output path for registration YAML")
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

	// Handle registration generation
	if *generateReg {
		generateRegistration(cfg, *regOutput)
		return
	}

	// Validate config
	if cfg.Matrix.Homeserver == "" {
		log.Fatal("Matrix homeserver is required (MATRIX_HOMESERVER)")
	}
	if len(cfg.Whitelist) == 0 {
		log.Fatal("At least one whitelisted user is required (MATRIX_WHITELIST)")
	}

	log.Printf("Starting Matrix-Claude Code integration in %s mode", cfg.Mode)
	log.Printf("Matrix homeserver: %s", cfg.Matrix.Homeserver)
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
	} else if cfg.IsAppServiceMode() {
		runAppServiceMode(ctx, cfg)
	} else {
		runBotMode(ctx, cfg)
	}

	log.Println("Integration stopped")
}

// runStdioMode runs as an MCP server for Claude Code --channels
// It can receive messages via:
// 1. IPC from Application Service mode (real-time push)
// 2. Direct Matrix polling (fallback if no AS)
func runStdioMode(ctx context.Context, cfg *config.Config) {
	log.Println("Running in MCP stdio mode for Claude Code channels")

	// Get socket path from config or use default
	socketPath := cfg.IPC.SocketPath
	if socketPath == "" {
		socketPath = ipc.DefaultSocketPath()
	}

	// Determine if we should use IPC (AS mode running separately) or direct Matrix polling
	useIPC := cfg.IPC.Enabled

	var clientAdapter matrix.ClientAdapter
	var matrixClient *mautrix.Client

	// If not using IPC, we need Matrix credentials for direct polling
	if !useIPC {
		if cfg.Matrix.UserID == "" {
			log.Fatal("Matrix user ID is required (MATRIX_USER_ID)")
		}
		if cfg.Matrix.AccessToken == "" {
			log.Fatal("Matrix access token is required (MATRIX_ACCESS_TOKEN)")
		}

		log.Printf("Bot user ID: %s", cfg.Matrix.UserID)

		// Create mautrix client for Matrix communication
		userID := id.UserID(cfg.Matrix.UserID)
		var err error
		matrixClient, err = mautrix.NewClient(cfg.Matrix.Homeserver, userID, cfg.Matrix.AccessToken)
		if err != nil {
			log.Fatalf("Failed to create Matrix client: %v", err)
		}

		if cfg.Matrix.DeviceID != "" {
			matrixClient.DeviceID = id.DeviceID(cfg.Matrix.DeviceID)
		}

		clientAdapter = matrix.NewBotClientAdapter(matrixClient)
	}

	// Create MCP server with reply handler
	var handler *matrix.Handler
	var ipcServer *ipc.Server

	mcpServer := channel.NewMCPServer(
		cfg.Channel.Name,
		"1.0.0",
		func(ctx context.Context, roomID, threadID, message string) error {
			if useIPC {
				// Send reply back via IPC to AS mode
				if ipcServer != nil {
					return ipcServer.BroadcastReply(&ipc.ReplyPayload{
						RoomID:   roomID,
						ThreadID: threadID,
						Content:  message,
					})
				}
				return nil
			}
			// Send reply directly to Matrix
			if handler != nil {
				return handler.HandleReply(ctx, roomID, threadID, message)
			}
			return nil
		},
	)

	// Create handler if using direct Matrix connection
	if !useIPC {
		handler = matrix.NewHandler(clientAdapter, mcpServer, cfg)
	}

	// Start IPC server if enabled
	if useIPC {
		ipcServer = ipc.NewServer(socketPath, func(ctx context.Context, event *ipc.MatrixEventPayload) {
			// Forward event to MCP server (which notifies Claude Code)
			log.Printf("IPC: Received event from AS mode, forwarding to Claude Code")
			err := mcpServer.PushMessage(ctx, event.RoomID, event.ThreadID, event.Sender, event.Content)
			if err != nil {
				log.Printf("Failed to push message to MCP: %v", err)
			}
		})

		go func() {
			log.Printf("Starting IPC server on %s...", socketPath)
			if err := ipcServer.Start(ctx); err != nil {
				if ctx.Err() == nil {
					log.Printf("IPC server error: %v", err)
				}
			}
		}()

		log.Printf("MCP Channel server ready. Waiting for events via IPC...")
	} else {
		// Set up syncer for direct Matrix events
		syncer := mautrix.NewDefaultSyncer()

		syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
			asEvent := convertToASEvent(evt)
			handler.HandleEvent(ctx, asEvent)
		})

		syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
			asEvent := convertToASEvent(evt)
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

		log.Printf("MCP Channel server ready. Listening for Matrix messages via polling...")
	}

	// Run MCP server (blocks until context is cancelled)
	if err := mcpServer.Run(ctx); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("MCP server error: %v", err)
		}
	}
}

// runAppServiceMode runs the integration as a Matrix Application Service
// Events are forwarded via IPC to the stdio mode (spawned by Claude Code)
func runAppServiceMode(ctx context.Context, cfg *config.Config) {
	log.Println("Running in Application Service mode with IPC")

	// Load or use config tokens
	var hsToken, asToken string
	botUserID := cfg.GetBotUserID()

	if cfg.AppService.RegistrationPath != "" {
		reg, err := appservice.LoadFromFile(cfg.AppService.RegistrationPath)
		if err != nil {
			log.Fatalf("Failed to load registration file: %v", err)
		}
		hsToken = reg.HSToken
		asToken = reg.ASToken
		botUserID = "@" + reg.SenderLocalpart + ":" + cfg.AppService.HomeserverDomain
	} else {
		hsToken = cfg.AppService.HSToken
		asToken = cfg.AppService.ASToken
	}

	if hsToken == "" || asToken == "" {
		log.Fatal("AS tokens are required. Either provide registration_path or hs_token/as_token in config")
	}

	if botUserID == "" {
		log.Fatal("Bot user ID could not be determined. Set sender_localpart and homeserver_domain")
	}

	log.Printf("Bot user ID: %s", botUserID)
	log.Printf("AS listen address: %s", cfg.AppService.ListenAddress)

	// Create AS client for sending messages back to Matrix
	asClient := appservice.NewClient(cfg.Matrix.Homeserver, asToken, botUserID)
	clientAdapter := matrix.NewASClientAdapter(asClient)

	// Get socket path from config or use default
	socketPath := cfg.IPC.SocketPath
	if socketPath == "" {
		socketPath = ipc.DefaultSocketPath()
	}

	// Create IPC client to forward events to stdio mode
	ipcClient := ipc.NewClient(socketPath, func(ctx context.Context, reply *ipc.ReplyPayload) {
		// Handle replies from stdio mode (forward to Matrix)
		log.Printf("IPC: Received reply for room %s", reply.RoomID)
		err := clientAdapter.SendMessage(ctx, reply.RoomID, reply.ThreadID, reply.Content)
		if err != nil {
			log.Printf("Failed to send reply to Matrix: %v", err)
		}
	})

	// Connect to IPC server (stdio mode)
	log.Printf("Connecting to IPC server at %s...", socketPath)
	if err := ipcClient.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect to IPC server: %v", err)
	}
	defer ipcClient.Close()

	// Create handler that forwards events via IPC
	forwarder := &ipcForwarder{client: ipcClient, cfg: cfg}

	// Create AS server
	asServer := appservice.NewServer(hsToken, asToken, botUserID, func(ctx context.Context, event *appservice.Event) {
		forwarder.HandleEvent(ctx, event)
	})

	log.Printf("Application Service is running. Press Ctrl+C to stop.")

	// Start the server (blocks until context is cancelled)
	if err := asServer.Start(ctx, cfg.AppService.ListenAddress); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("AS server error: %v", err)
		}
	}
}

// ipcForwarder forwards Matrix events to the stdio mode via IPC
type ipcForwarder struct {
	client *ipc.Client
	cfg    *config.Config
}

func (f *ipcForwarder) HandleEvent(ctx context.Context, event *appservice.Event) {
	// Only handle message events
	if event.Type != "m.room.message" {
		return
	}

	// Check whitelist
	if !f.cfg.IsWhitelisted(event.Sender) {
		log.Printf("Ignoring message from non-whitelisted user: %s", event.Sender)
		return
	}

	// Extract message content
	content, ok := event.Content["body"].(string)
	if !ok || content == "" {
		return
	}

	// Extract thread ID if present
	var threadID string
	if relatesTo, ok := event.Content["m.relates_to"].(map[string]interface{}); ok {
		if rel, ok := relatesTo["rel_type"].(string); ok && rel == "m.thread" {
			if evtID, ok := relatesTo["event_id"].(string); ok {
				threadID = evtID
			}
		}
	}

	log.Printf("IPC: Forwarding message from %s to stdio mode", event.Sender)

	// Forward to stdio mode via IPC
	err := f.client.SendEvent(&ipc.MatrixEventPayload{
		RoomID:    event.RoomID,
		EventID:   event.EventID,
		Sender:    event.Sender,
		Content:   content,
		ThreadID:  threadID,
		Timestamp: event.OriginServerTS,
	})
	if err != nil {
		log.Printf("Failed to forward event via IPC: %v", err)
	}
}

// runBotMode runs the integration as a regular Matrix bot (polling)
func runBotMode(ctx context.Context, cfg *config.Config) {
	log.Println("Running in Bot mode (fallback)")

	if cfg.Matrix.UserID == "" {
		log.Fatal("Matrix user ID is required in bot mode (MATRIX_USER_ID)")
	}
	if cfg.Matrix.AccessToken == "" {
		log.Fatal("Matrix access token is required in bot mode (MATRIX_ACCESS_TOKEN)")
	}

	log.Printf("Bot user ID: %s", cfg.Matrix.UserID)

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

	// Create MCP server with reply handler
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
	)

	// Create handler
	handler = matrix.NewHandler(clientAdapter, mcpServer, cfg)

	// Set up syncer
	syncer := mautrix.NewDefaultSyncer()

	// Handle message events
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		// Convert to appservice.Event format
		asEvent := convertToASEvent(evt)
		handler.HandleEvent(ctx, asEvent)
	})

	// Handle member events (for invites)
	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		asEvent := convertToASEvent(evt)
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

// convertToASEvent converts a mautrix event to appservice.Event format
func convertToASEvent(evt *event.Event) *appservice.Event {
	var stateKey *string
	if evt.StateKey != nil {
		sk := *evt.StateKey
		stateKey = &sk
	}

	return &appservice.Event{
		EventID:        string(evt.ID),
		RoomID:         string(evt.RoomID),
		Sender:         string(evt.Sender),
		Type:           evt.Type.Type,
		StateKey:       stateKey,
		Content:        evt.Content.Raw,
		OriginServerTS: evt.Timestamp,
	}
}

// generateRegistration generates an AS registration YAML file
func generateRegistration(cfg *config.Config, outputPath string) {
	if cfg.AppService.HomeserverDomain == "" {
		log.Fatal("Homeserver domain is required for registration generation (AS_HOMESERVER_DOMAIN)")
	}

	publicURL := cfg.AppService.PublicURL
	if publicURL == "" {
		publicURL = "http://localhost" + cfg.AppService.ListenAddress
	}

	reg, err := appservice.NewRegistration(
		cfg.AppService.ID,
		publicURL,
		cfg.AppService.SenderLocalpart,
		cfg.AppService.HomeserverDomain,
	)
	if err != nil {
		log.Fatalf("Failed to create registration: %v", err)
	}

	if err := reg.SaveToFile(outputPath); err != nil {
		log.Fatalf("Failed to save registration: %v", err)
	}

	log.Printf("Registration saved to: %s", outputPath)
	log.Printf("AS Token: %s", reg.ASToken)
	log.Printf("HS Token: %s", reg.HSToken)
	log.Printf("Bot User ID: @%s:%s", reg.SenderLocalpart, cfg.AppService.HomeserverDomain)
	log.Println("")
	log.Println("Next steps:")
	log.Printf("1. Copy %s to your homeserver's AS configuration directory", outputPath)
	log.Println("2. Add the registration to your homeserver config (e.g., Synapse's app_service_config_files)")
	log.Println("3. Restart your homeserver")
	log.Println("4. Set AS_HS_TOKEN and AS_TOKEN in your environment (or use --config with registration_path)")
	log.Println("5. Start the integration")
}
