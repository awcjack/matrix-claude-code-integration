package matrix

import (
	"context"

	"github.com/personal/matrix-claude-code-integration/internal/appservice"
)

// ASClientAdapter adapts appservice.Client to the MatrixClient interface
type ASClientAdapter struct {
	client *appservice.Client
}

// NewASClientAdapter creates a new adapter for the AS client
func NewASClientAdapter(client *appservice.Client) *ASClientAdapter {
	return &ASClientAdapter{client: client}
}

// SendMessage sends a text message to a room
func (a *ASClientAdapter) SendMessage(ctx context.Context, roomID, message string) (string, error) {
	resp, err := a.client.SendMessage(ctx, roomID, message)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// SendLiveMessage sends a message with MSC4357 live flag
func (a *ASClientAdapter) SendLiveMessage(ctx context.Context, roomID, threadID, message string) (string, error) {
	resp, err := a.client.SendLiveMessage(ctx, roomID, threadID, message)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// EditMessage edits an existing message
func (a *ASClientAdapter) EditMessage(ctx context.Context, roomID, eventID, newContent string, isLive bool) error {
	return a.client.EditMessage(ctx, roomID, eventID, newContent, isLive)
}

// SendReply sends a reply in a thread
func (a *ASClientAdapter) SendReply(ctx context.Context, roomID, threadID, replyTo, message string, isNotice bool) (string, error) {
	resp, err := a.client.SendReply(ctx, roomID, threadID, replyTo, message, isNotice)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// SetTyping sets the typing indicator
func (a *ASClientAdapter) SetTyping(ctx context.Context, roomID string, typing bool, timeoutMS int) error {
	return a.client.SetTyping(ctx, roomID, typing, timeoutMS)
}

// JoinRoom joins a room by ID
func (a *ASClientAdapter) JoinRoom(ctx context.Context, roomID string) error {
	_, err := a.client.JoinRoom(ctx, roomID)
	return err
}

// GetBotUserID returns the bot's user ID
func (a *ASClientAdapter) GetBotUserID() string {
	return a.client.GetBotUserID()
}
