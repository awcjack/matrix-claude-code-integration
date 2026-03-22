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
func runStdioMode(ctx context.Context, cfg *config.Config) {
	log.Println("Running in MCP stdio mode for Claude Code channels")

	// Create MCP server
	mcpServer := channel.NewMCPServer(
		cfg.Channel.Name,
		"1.0.0",
		nil, // Reply handler will be set up with Matrix client
	)

	// For stdio mode, we need to handle Matrix separately
	// This is a bridge mode where:
	// 1. Claude Code spawns this as an MCP server
	// 2. This server receives Matrix messages and pushes them as channel notifications
	// 3. Claude's replies come back through the reply tool

	// In a full implementation, this would also start the Matrix client
	// in a separate goroutine to receive messages
	log.Printf("MCP Channel server ready. Waiting for messages...")

	if err := mcpServer.Run(ctx); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("MCP server error: %v", err)
		}
	}
}

// runAppServiceMode runs the integration as a Matrix Application Service
func runAppServiceMode(ctx context.Context, cfg *config.Config) {
	log.Println("Running in Application Service mode")

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

	// Create AS client for sending messages
	asClient := appservice.NewClient(cfg.Matrix.Homeserver, asToken, botUserID)

	// Create adapter
	clientAdapter := matrix.NewASClientAdapter(asClient)

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

	// Create AS server
	asServer := appservice.NewServer(hsToken, asToken, botUserID, func(ctx context.Context, event *appservice.Event) {
		handler.HandleEvent(ctx, event)
	})

	log.Printf("Application Service is running. Press Ctrl+C to stop.")

	// Start the server (blocks until context is cancelled)
	if err := asServer.Start(ctx, cfg.AppService.ListenAddress); err != nil {
		if ctx.Err() == nil {
			log.Fatalf("AS server error: %v", err)
		}
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
