package matrix

import "context"

// MatrixClient is an interface for sending messages to Matrix
// This interface is used by both coordinator and the legacy single-session handler
type MatrixClient interface {
	SendMessage(ctx context.Context, roomID, message string) (string, error)
	SendLiveMessage(ctx context.Context, roomID, threadID, message string) (string, error)
	EditMessage(ctx context.Context, roomID, eventID, newContent string, isLive bool) error
	SendReply(ctx context.Context, roomID, threadID, replyTo, message string, isNotice bool) (string, error)
	SetTyping(ctx context.Context, roomID string, typing bool, timeoutMS int) error
	JoinRoom(ctx context.Context, roomID string) error
	GetBotUserID() string
}
