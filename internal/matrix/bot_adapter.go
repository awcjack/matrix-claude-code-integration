package matrix

import (
	"context"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// BotClientAdapter adapts mautrix.Client to the MatrixClient interface
type BotClientAdapter struct {
	client *mautrix.Client
}

// NewBotClientAdapter creates a new adapter for the mautrix client
func NewBotClientAdapter(client *mautrix.Client) *BotClientAdapter {
	return &BotClientAdapter{client: client}
}

// SendMessage sends a text message to a room
func (a *BotClientAdapter) SendMessage(ctx context.Context, roomID, message string) (string, error) {
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    message,
	}
	resp, err := a.client.SendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, content)
	if err != nil {
		return "", err
	}
	return string(resp.EventID), nil
}

// SendLiveMessage sends a message with MSC4357 live flag
func (a *BotClientAdapter) SendLiveMessage(ctx context.Context, roomID, threadID, message string) (string, error) {
	content := map[string]interface{}{
		"msgtype":                 "m.text",
		"body":                    message,
		"org.matrix.msc4357.live": map[string]interface{}{},
	}

	if threadID != "" {
		content["m.relates_to"] = map[string]interface{}{
			"rel_type": "m.thread",
			"event_id": threadID,
		}
	}

	resp, err := a.client.SendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, content)
	if err != nil {
		return "", err
	}
	return string(resp.EventID), nil
}

// EditMessage edits an existing message
func (a *BotClientAdapter) EditMessage(ctx context.Context, roomID, eventID, newContent string, isLive bool) error {
	newContentMap := map[string]interface{}{
		"msgtype": "m.text",
		"body":    newContent,
	}

	if isLive {
		newContentMap["org.matrix.msc4357.live"] = map[string]interface{}{}
	}

	content := map[string]interface{}{
		"msgtype":       "m.text",
		"body":          "* " + newContent,
		"m.new_content": newContentMap,
		"m.relates_to": map[string]interface{}{
			"rel_type": "m.replace",
			"event_id": eventID,
		},
	}

	_, err := a.client.SendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, content)
	return err
}

// SendReply sends a reply in a thread
func (a *BotClientAdapter) SendReply(ctx context.Context, roomID, threadID, replyTo, message string, isNotice bool) (string, error) {
	msgType := event.MsgText
	if isNotice {
		msgType = event.MsgNotice
	}

	content := &event.MessageEventContent{
		MsgType: msgType,
		Body:    message,
	}

	if threadID != "" {
		content.RelatesTo = &event.RelatesTo{}
		content.RelatesTo.SetThread(id.EventID(threadID), id.EventID(replyTo))
	} else if replyTo != "" {
		content.RelatesTo = &event.RelatesTo{
			InReplyTo: &event.InReplyTo{
				EventID: id.EventID(replyTo),
			},
		}
	}

	resp, err := a.client.SendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, content)
	if err != nil {
		return "", err
	}
	return string(resp.EventID), nil
}

// SetTyping sets the typing indicator
func (a *BotClientAdapter) SetTyping(ctx context.Context, roomID string, typing bool, timeoutMS int) error {
	_, err := a.client.UserTyping(ctx, id.RoomID(roomID), typing, time.Duration(timeoutMS)*time.Millisecond)
	return err
}

// JoinRoom joins a room by ID
func (a *BotClientAdapter) JoinRoom(ctx context.Context, roomID string) error {
	_, err := a.client.JoinRoomByID(ctx, id.RoomID(roomID))
	return err
}

// GetBotUserID returns the bot's user ID
func (a *BotClientAdapter) GetBotUserID() string {
	return string(a.client.UserID)
}
