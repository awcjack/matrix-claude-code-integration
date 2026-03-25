// Coordinator is the parent server that manages Matrix-Claude Code sessions
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/matrix-claude-code/appservice/appservice"
	"github.com/anthropics/matrix-claude-code/appservice/coordinator"
	"github.com/anthropics/matrix-claude-code/appservice/matrix"
)

// Config holds the coordinator configuration
type Config struct {
	Matrix struct {
		Homeserver string `json:"homeserver"`
		AppService struct {
			RegistrationPath string `json:"registration_path"`
			ListenAddress    string `json:"listen_address"`
			HSToken          string `json:"hs_token"`
			ASToken          string `json:"as_token"`
			BotUserID        string `json:"bot_user_id"`
		} `json:"appservice"`
	} `json:"matrix"`

	Sessions struct {
		WorkingDirectory string `json:"working_directory"`
		Model            string `json:"model"`
		SystemPrompt     string `json:"system_prompt"`
		MaxConcurrent    int    `json:"max_concurrent"`
		IdleTimeout      string `json:"idle_timeout"`
	} `json:"sessions"`

	IPC struct {
		SocketPath string `json:"socket_path"`
	} `json:"ipc"`

	Bridge struct {
		Path string `json:"path"`
	} `json:"bridge"`

	Whitelist []string `json:"whitelist"`
}

func main() {
	configPath := flag.String("config", "config.json", "Path to config file")
	flag.Parse()

	// Load config
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Apply environment overrides
	applyEnvOverrides(cfg)

	// Validate config
	if err := validateConfig(cfg); err != nil {
		log.Fatalf("Invalid config: %v", err)
	}

	// Parse idle timeout
	idleTimeout := 30 * time.Minute
	if cfg.Sessions.IdleTimeout != "" {
		if d, err := time.ParseDuration(cfg.Sessions.IdleTimeout); err == nil {
			idleTimeout = d
		}
	}

	// Create Matrix client
	asClient := appservice.NewClient(
		cfg.Matrix.Homeserver,
		cfg.Matrix.AppService.ASToken,
		cfg.Matrix.AppService.BotUserID,
	)
	matrixClient := matrix.NewClientAdapter(asClient)

	// Create coordinator server
	serverConfig := coordinator.ServerConfig{
		ListenAddress: cfg.Matrix.AppService.ListenAddress,
		HSToken:       cfg.Matrix.AppService.HSToken,
		ASToken:       cfg.Matrix.AppService.ASToken,
		BotUserID:     cfg.Matrix.AppService.BotUserID,
		BridgePath:    cfg.Bridge.Path,
		SocketPath:    cfg.IPC.SocketPath,
		Whitelist:     cfg.Whitelist,
		IdleTimeout:   idleTimeout,
		SessionConfig: coordinator.SessionConfig{
			WorkingDirectory: cfg.Sessions.WorkingDirectory,
			Model:            cfg.Sessions.Model,
			SystemPrompt:     cfg.Sessions.SystemPrompt,
		},
	}

	server := coordinator.NewServer(serverConfig, matrixClient)

	// Start server
	if err := server.Start(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	log.Printf("Coordinator running on %s", cfg.Matrix.AppService.ListenAddress)
	log.Printf("IPC socket: %s", cfg.IPC.SocketPath)
	log.Printf("Bridge path: %s", cfg.Bridge.Path)
	log.Printf("Whitelisted users: %d", len(cfg.Whitelist))

	// Wait for shutdown signal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		log.Printf("Received signal: %v", sig)
	case <-ctx.Done():
	}

	// Graceful shutdown
	log.Println("Shutting down coordinator...")
	if err := server.Stop(); err != nil {
		log.Printf("Shutdown error: %v", err)
	}

	log.Println("Coordinator stopped")
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("MATRIX_HOMESERVER"); v != "" {
		cfg.Matrix.Homeserver = v
	}
	if v := os.Getenv("APPSERVICE_HS_TOKEN"); v != "" {
		cfg.Matrix.AppService.HSToken = v
	}
	if v := os.Getenv("APPSERVICE_AS_TOKEN"); v != "" {
		cfg.Matrix.AppService.ASToken = v
	}
	if v := os.Getenv("APPSERVICE_BOT_USER_ID"); v != "" {
		cfg.Matrix.AppService.BotUserID = v
	}
	if v := os.Getenv("APPSERVICE_LISTEN_ADDRESS"); v != "" {
		cfg.Matrix.AppService.ListenAddress = v
	}
	if v := os.Getenv("SESSION_WORKING_DIRECTORY"); v != "" {
		cfg.Sessions.WorkingDirectory = v
	}
	if v := os.Getenv("SESSION_MODEL"); v != "" {
		cfg.Sessions.Model = v
	}
	if v := os.Getenv("IPC_SOCKET_PATH"); v != "" {
		cfg.IPC.SocketPath = v
	}
	if v := os.Getenv("BRIDGE_PATH"); v != "" {
		cfg.Bridge.Path = v
	}
}

func validateConfig(cfg *Config) error {
	if cfg.Matrix.Homeserver == "" {
		return &configError{"matrix.homeserver is required"}
	}
	if cfg.Matrix.AppService.HSToken == "" {
		return &configError{"matrix.appservice.hs_token is required"}
	}
	if cfg.Matrix.AppService.ASToken == "" {
		return &configError{"matrix.appservice.as_token is required"}
	}
	if cfg.Matrix.AppService.BotUserID == "" {
		return &configError{"matrix.appservice.bot_user_id is required"}
	}
	if cfg.Matrix.AppService.ListenAddress == "" {
		cfg.Matrix.AppService.ListenAddress = ":8080"
	}
	if cfg.IPC.SocketPath == "" {
		cfg.IPC.SocketPath = "/tmp/matrix-claude.sock"
	}
	if cfg.Bridge.Path == "" {
		cfg.Bridge.Path = "matrix-bridge"
	}
	if len(cfg.Whitelist) == 0 {
		return &configError{"whitelist is required"}
	}
	if cfg.Sessions.Model == "" {
		cfg.Sessions.Model = "sonnet"
	}
	return nil
}

type configError struct {
	msg string
}

func (e *configError) Error() string {
	return e.msg
}
