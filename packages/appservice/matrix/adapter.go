package matrix

import (
	"context"

	"github.com/anthropics/matrix-claude-code/appservice/appservice"
)

// ClientAdapter wraps appservice.Client to implement MatrixClient interface
type ClientAdapter struct {
	client *appservice.Client
}

// NewClientAdapter creates a new adapter for appservice.Client
func NewClientAdapter(client *appservice.Client) *ClientAdapter {
	return &ClientAdapter{client: client}
}

// SendMessage sends a text message to a room
func (a *ClientAdapter) SendMessage(ctx context.Context, roomID, message string) (string, error) {
	resp, err := a.client.SendMessage(ctx, roomID, message)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// SendReply sends a reply in a thread
func (a *ClientAdapter) SendReply(ctx context.Context, roomID, threadID, replyTo, message string, isNotice bool) (string, error) {
	resp, err := a.client.SendReply(ctx, roomID, threadID, replyTo, message, isNotice)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// SetTyping sets the typing indicator
func (a *ClientAdapter) SetTyping(ctx context.Context, roomID string, typing bool, timeoutMS int) error {
	return a.client.SetTyping(ctx, roomID, typing, timeoutMS)
}

// JoinRoom joins a room
func (a *ClientAdapter) JoinRoom(ctx context.Context, roomID string) error {
	_, err := a.client.JoinRoom(ctx, roomID)
	return err
}

// GetBotUserID returns the bot's user ID
func (a *ClientAdapter) GetBotUserID() string {
	return a.client.GetBotUserID()
}

// SendLiveMessage sends a message with MSC4357 live flag
func (a *ClientAdapter) SendLiveMessage(ctx context.Context, roomID, threadID, message string) (string, error) {
	resp, err := a.client.SendLiveMessage(ctx, roomID, threadID, message)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// EditMessage edits an existing message
func (a *ClientAdapter) EditMessage(ctx context.Context, roomID, eventID, newContent string, isLive bool) error {
	return a.client.EditMessage(ctx, roomID, eventID, newContent, isLive)
}
