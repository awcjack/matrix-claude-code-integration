// Package matrix provides the Matrix client interface for the appservice
package matrix

import "context"

// MatrixClient is an interface for sending messages to Matrix
type MatrixClient interface {
	SendMessage(ctx context.Context, roomID, message string) (string, error)
	SendReply(ctx context.Context, roomID, threadID, replyTo, message string, isNotice bool) (string, error)
	SetTyping(ctx context.Context, roomID string, typing bool, timeoutMS int) error
	JoinRoom(ctx context.Context, roomID string) error
	GetBotUserID() string

	// Streaming message support (MSC4357)
	SendLiveMessage(ctx context.Context, roomID, threadID, message string) (string, error)
	EditMessage(ctx context.Context, roomID, eventID, newContent string, isLive bool) error
}
