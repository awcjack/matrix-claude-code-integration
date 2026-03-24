package appservice

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Registration represents the Application Service registration YAML
type Registration struct {
	ID              string     `yaml:"id"`
	URL             string     `yaml:"url"`
	ASToken         string     `yaml:"as_token"`
	HSToken         string     `yaml:"hs_token"`
	SenderLocalpart string     `yaml:"sender_localpart"`
	RateLimited     *bool      `yaml:"rate_limited,omitempty"`
	Namespaces      Namespaces `yaml:"namespaces"`
	// Optional: receive ephemeral events (typing, presence, receipts)
	ReceiveEphemeral bool `yaml:"de.sorunome.msc2409.push_ephemeral,omitempty"`
}

// Namespaces defines the user/room/alias namespaces the AS manages
type Namespaces struct {
	Users   []NamespaceEntry `yaml:"users"`
	Rooms   []NamespaceEntry `yaml:"rooms,omitempty"`
	Aliases []NamespaceEntry `yaml:"aliases,omitempty"`
}

// NamespaceEntry represents a single namespace pattern
type NamespaceEntry struct {
	Exclusive bool   `yaml:"exclusive"`
	Regex     string `yaml:"regex"`
}

// GenerateToken generates a secure random token
func GenerateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// NewRegistration creates a new AS registration with generated tokens
func NewRegistration(id, url, senderLocalpart, homeserverDomain string) (*Registration, error) {
	asToken, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("generate as_token: %w", err)
	}

	hsToken, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("generate hs_token: %w", err)
	}

	rateLimited := false

	return &Registration{
		ID:              id,
		URL:             url,
		ASToken:         asToken,
		HSToken:         hsToken,
		SenderLocalpart: senderLocalpart,
		RateLimited:     &rateLimited,
		Namespaces: Namespaces{
			Users: []NamespaceEntry{
				{
					// Match the bot user
					Exclusive: true,
					Regex:     fmt.Sprintf("@%s:%s", senderLocalpart, escapeRegex(homeserverDomain)),
				},
			},
			// Empty rooms/aliases means we're interested in all rooms we're invited to
			Rooms:   []NamespaceEntry{},
			Aliases: []NamespaceEntry{},
		},
	}, nil
}

// SaveToFile saves the registration to a YAML file
func (r *Registration) SaveToFile(path string) error {
	data, err := yaml.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal registration: %w", err)
	}

	if err := os.WriteFile(path, data, 0640); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// LoadFromFile loads a registration from a YAML file
func LoadFromFile(path string) (*Registration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var reg Registration
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("unmarshal registration: %w", err)
	}

	return &reg, nil
}

// escapeRegex escapes special regex characters in a string
func escapeRegex(s string) string {
	special := []byte{'\\', '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$'}
	result := make([]byte, 0, len(s)*2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		for _, sp := range special {
			if c == sp {
				result = append(result, '\\')
				break
			}
		}
		result = append(result, c)
	}
	return string(result)
}
