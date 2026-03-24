package config

import (
	"encoding/json"
	"os"
)

// Mode represents the Matrix connection mode
type Mode string

const (
	// ModeAppService uses the Application Service API (push-based, requires HS admin)
	ModeAppService Mode = "appservice"
	// ModeBot uses the Client-Server API with polling (fallback, no admin required)
	ModeBot Mode = "bot"
)

// Config holds the application configuration
type Config struct {
	// Mode: "appservice" (default) or "bot"
	Mode Mode `json:"mode"`

	// Matrix configuration
	Matrix MatrixConfig `json:"matrix"`

	// Application Service configuration (used when mode=appservice)
	AppService AppServiceConfig `json:"appservice,omitempty"`

	// Claude Code configuration
	ClaudeCode ClaudeCodeConfig `json:"claude_code"`

	// Whitelist of allowed Matrix user IDs
	Whitelist []string `json:"whitelist"`

	// Channel settings
	Channel ChannelConfig `json:"channel"`
}

// MatrixConfig holds Matrix-specific configuration
type MatrixConfig struct {
	// Homeserver URL (e.g., https://matrix.org)
	Homeserver string `json:"homeserver"`

	// Bot user ID (e.g., @claude-bot:matrix.org)
	UserID string `json:"user_id"`

	// Access token for authentication (only used in bot mode)
	AccessToken string `json:"access_token,omitempty"`

	// Device ID (optional, only used in bot mode)
	DeviceID string `json:"device_id,omitempty"`
}

// AppServiceConfig holds Application Service specific configuration
type AppServiceConfig struct {
	// ID is the unique identifier for this AS
	ID string `json:"id"`

	// RegistrationPath is the path to the registration YAML file
	RegistrationPath string `json:"registration_path"`

	// ListenAddress is the address to listen for HS callbacks (e.g., ":8080")
	ListenAddress string `json:"listen_address"`

	// PublicURL is the URL the homeserver uses to reach this AS
	PublicURL string `json:"public_url"`

	// HSToken is the token for the homeserver to authenticate to us
	HSToken string `json:"hs_token,omitempty"`

	// ASToken is the token for us to authenticate to the homeserver
	ASToken string `json:"as_token,omitempty"`

	// SenderLocalpart is the localpart of the bot user (e.g., "claude-bot")
	SenderLocalpart string `json:"sender_localpart"`

	// HomeserverDomain is the domain part of the homeserver (e.g., "matrix.example.com")
	HomeserverDomain string `json:"homeserver_domain"`
}

// ClaudeCodeConfig holds Claude Code specific configuration
type ClaudeCodeConfig struct {
	// WorkingDirectory is the directory where Claude Code operates
	WorkingDirectory string `json:"working_directory"`

	// Model to use (e.g., "claude-sonnet-4-6-20250514", "claude-opus-4-6-20250514")
	Model string `json:"model,omitempty"`

	// SystemPrompt is an optional system prompt prefix
	SystemPrompt string `json:"system_prompt,omitempty"`

	// AllowedTools is a list of allowed tool patterns (empty means all allowed)
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// DisallowedTools is a list of disallowed tool patterns
	DisallowedTools []string `json:"disallowed_tools,omitempty"`

	// MaxTurns limits the number of agentic turns per message
	MaxTurns int `json:"max_turns,omitempty"`

	// PermissionMode controls permission behavior
	// Options: "default", "plan", "auto-approve"
	PermissionMode string `json:"permission_mode,omitempty"`
}

// ChannelConfig holds MCP channel server settings
type ChannelConfig struct {
	// Name is the channel name shown in Claude Code (default: "matrix")
	Name string `json:"name"`

	// StdioMode runs as an MCP stdio server (for --channels flag)
	StdioMode bool `json:"stdio_mode"`

	// HTTPPort for standalone HTTP webhook receiver mode
	HTTPPort int `json:"http_port,omitempty"`
}

// Load reads configuration from a JSON file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Set defaults
	cfg.setDefaults()

	return &cfg, nil
}

// LoadFromEnv reads configuration from environment variables
func LoadFromEnv() *Config {
	mode := Mode(os.Getenv("MATRIX_MODE"))

	cfg := &Config{
		Mode: mode,
		Matrix: MatrixConfig{
			Homeserver:  os.Getenv("MATRIX_HOMESERVER"),
			UserID:      os.Getenv("MATRIX_USER_ID"),
			AccessToken: os.Getenv("MATRIX_ACCESS_TOKEN"),
			DeviceID:    os.Getenv("MATRIX_DEVICE_ID"),
		},
		AppService: AppServiceConfig{
			ID:               os.Getenv("AS_ID"),
			RegistrationPath: os.Getenv("AS_REGISTRATION_PATH"),
			ListenAddress:    os.Getenv("AS_LISTEN_ADDRESS"),
			PublicURL:        os.Getenv("AS_PUBLIC_URL"),
			HSToken:          os.Getenv("AS_HS_TOKEN"),
			ASToken:          os.Getenv("AS_TOKEN"),
			SenderLocalpart:  os.Getenv("AS_SENDER_LOCALPART"),
			HomeserverDomain: os.Getenv("AS_HOMESERVER_DOMAIN"),
		},
		ClaudeCode: ClaudeCodeConfig{
			WorkingDirectory: getEnvDefault("CLAUDE_CODE_WORKING_DIR", "."),
			Model:            os.Getenv("CLAUDE_CODE_MODEL"),
			SystemPrompt:     os.Getenv("CLAUDE_CODE_SYSTEM_PROMPT"),
			PermissionMode:   os.Getenv("CLAUDE_CODE_PERMISSION_MODE"),
		},
		Channel: ChannelConfig{
			Name:      getEnvDefault("CHANNEL_NAME", "matrix"),
			StdioMode: os.Getenv("CHANNEL_STDIO_MODE") == "true",
			HTTPPort:  parseIntDefault(os.Getenv("CHANNEL_HTTP_PORT"), 0),
		},
		Whitelist: parseWhitelist(os.Getenv("MATRIX_WHITELIST")),
	}

	cfg.setDefaults()
	return cfg
}

// setDefaults applies default values
func (c *Config) setDefaults() {
	// Default mode is appservice
	if c.Mode == "" {
		c.Mode = ModeAppService
	}

	if c.AppService.ID == "" {
		c.AppService.ID = "claude-code-bridge"
	}

	if c.AppService.ListenAddress == "" {
		c.AppService.ListenAddress = ":8080"
	}

	if c.AppService.SenderLocalpart == "" {
		c.AppService.SenderLocalpart = "claude-bot"
	}

	if c.Channel.Name == "" {
		c.Channel.Name = "matrix"
	}

	if c.ClaudeCode.WorkingDirectory == "" {
		c.ClaudeCode.WorkingDirectory = "."
	}
}

// IsAppServiceMode returns true if running in Application Service mode
func (c *Config) IsAppServiceMode() bool {
	return c.Mode == ModeAppService
}

// IsBotMode returns true if running in bot (client API) mode
func (c *Config) IsBotMode() bool {
	return c.Mode == ModeBot
}

// GetBotUserID returns the full bot user ID
func (c *Config) GetBotUserID() string {
	if c.Matrix.UserID != "" {
		return c.Matrix.UserID
	}
	if c.AppService.SenderLocalpart != "" && c.AppService.HomeserverDomain != "" {
		return "@" + c.AppService.SenderLocalpart + ":" + c.AppService.HomeserverDomain
	}
	return ""
}

// Validate checks if the configuration is valid for the selected mode
func (c *Config) Validate() error {
	if c.IsAppServiceMode() {
		return c.validateAppServiceMode()
	}
	return c.validateBotMode()
}

func (c *Config) validateAppServiceMode() error {
	// For AS mode, we need either:
	// 1. A registration file path, OR
	// 2. HS token, AS token, and other AS config
	if c.AppService.RegistrationPath != "" {
		return nil
	}
	if c.AppService.HSToken != "" && c.AppService.ASToken != "" {
		return nil
	}
	// Will generate registration on first run
	return nil
}

func (c *Config) validateBotMode() error {
	// For bot mode, we need access token
	if c.Matrix.AccessToken == "" {
		return &ConfigError{Field: "matrix.access_token", Message: "required in bot mode"}
	}
	return nil
}

// ConfigError represents a configuration validation error
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return e.Field + ": " + e.Message
}

func getEnvDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func parseIntDefault(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	var n int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

func parseWhitelist(s string) []string {
	if s == "" {
		return nil
	}
	var whitelist []string
	if err := json.Unmarshal([]byte(s), &whitelist); err != nil {
		// Try comma-separated format
		return splitAndTrim(s)
	}
	return whitelist
}

func splitAndTrim(s string) []string {
	var result []string
	for _, part := range splitString(s, ',') {
		trimmed := trimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func splitString(s string, sep rune) []string {
	var result []string
	var current []rune
	for _, r := range s {
		if r == sep {
			result = append(result, string(current))
			current = nil
		} else {
			current = append(current, r)
		}
	}
	result = append(result, string(current))
	return result
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// IsUserWhitelisted checks if a user ID is in the whitelist
func (c *Config) IsUserWhitelisted(userID string) bool {
	if len(c.Whitelist) == 0 {
		return false // No whitelist means deny all
	}
	for _, allowed := range c.Whitelist {
		if allowed == userID || allowed == "*" {
			return true
		}
	}
	return false
}

