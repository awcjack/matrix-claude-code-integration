package appservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client provides Matrix Client-Server API access for the Application Service
type Client struct {
	homeserverURL string
	asToken       string
	botUserID     string
	httpClient    *http.Client
}

// NewClient creates a new AS client for Matrix API calls
func NewClient(homeserverURL, asToken, botUserID string) *Client {
	return &Client{
		homeserverURL: strings.TrimSuffix(homeserverURL, "/"),
		asToken:       asToken,
		botUserID:     botUserID,
		httpClient:    &http.Client{},
	}
}

// SendEventResponse is returned when sending an event
type SendEventResponse struct {
	EventID string `json:"event_id"`
}

// doRequest performs an authenticated request to the homeserver
func (c *Client) doRequest(ctx context.Context, method, path string, query url.Values, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	// Build URL with query parameters
	reqURL := c.homeserverURL + path
	if query != nil && len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.asToken)

	return c.httpClient.Do(req)
}

// SendMessage sends a text message to a room
func (c *Client) SendMessage(ctx context.Context, roomID, message string) (*SendEventResponse, error) {
	return c.SendMessageEvent(ctx, roomID, "m.room.message", map[string]interface{}{
		"msgtype": "m.text",
		"body":    message,
	})
}

// SendMessageEvent sends a message event to a room
func (c *Client) SendMessageEvent(ctx context.Context, roomID, eventType string, content interface{}) (*SendEventResponse, error) {
	txnID := fmt.Sprintf("%d", time.Now().UnixNano())
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/%s/%s",
		url.PathEscape(roomID),
		url.PathEscape(eventType),
		url.PathEscape(txnID))

	resp, err := c.doRequest(ctx, http.MethodPut, path, nil, content)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("send event failed: %s - %s", resp.Status, string(body))
	}

	var result SendEventResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// SendLiveMessage sends a message with MSC4357 live flag
func (c *Client) SendLiveMessage(ctx context.Context, roomID, threadID, message string) (*SendEventResponse, error) {
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

	return c.SendMessageEvent(ctx, roomID, "m.room.message", content)
}

// EditMessage edits an existing message
func (c *Client) EditMessage(ctx context.Context, roomID, eventID, newContent string, isLive bool) error {
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

	_, err := c.SendMessageEvent(ctx, roomID, "m.room.message", content)
	return err
}

// SendReply sends a reply in a thread
func (c *Client) SendReply(ctx context.Context, roomID, threadID, replyTo, message string, isNotice bool) (*SendEventResponse, error) {
	msgType := "m.text"
	if isNotice {
		msgType = "m.notice"
	}

	content := map[string]interface{}{
		"msgtype": msgType,
		"body":    message,
	}

	if threadID != "" {
		content["m.relates_to"] = map[string]interface{}{
			"rel_type": "m.thread",
			"event_id": threadID,
		}
	} else if replyTo != "" {
		content["m.relates_to"] = map[string]interface{}{
			"m.in_reply_to": map[string]interface{}{
				"event_id": replyTo,
			},
		}
	}

	return c.SendMessageEvent(ctx, roomID, "m.room.message", content)
}

// SetTyping sets the typing indicator for the bot
func (c *Client) SetTyping(ctx context.Context, roomID string, typing bool, timeoutMS int) error {
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/typing/%s",
		url.PathEscape(roomID),
		url.PathEscape(c.botUserID))

	body := map[string]interface{}{
		"typing": typing,
	}
	if typing && timeoutMS > 0 {
		body["timeout"] = timeoutMS
	}

	resp, err := c.doRequest(ctx, http.MethodPut, path, nil, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set typing failed: %s - %s", resp.Status, string(bodyBytes))
	}

	return nil
}

// JoinRoom joins a room by ID or alias
func (c *Client) JoinRoom(ctx context.Context, roomIDOrAlias string) (string, error) {
	path := fmt.Sprintf("/_matrix/client/v3/join/%s", url.PathEscape(roomIDOrAlias))

	resp, err := c.doRequest(ctx, http.MethodPost, path, nil, map[string]interface{}{})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("join room failed: %s - %s", resp.Status, string(body))
	}

	var result struct {
		RoomID string `json:"room_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return result.RoomID, nil
}

// GetDisplayName gets a user's display name
func (c *Client) GetDisplayName(ctx context.Context, userID string) (string, error) {
	path := fmt.Sprintf("/_matrix/client/v3/profile/%s/displayname", url.PathEscape(userID))

	resp, err := c.doRequest(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get displayname failed: %s", resp.Status)
	}

	var result struct {
		DisplayName string `json:"displayname"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return result.DisplayName, nil
}

// SetDisplayName sets the bot's display name
func (c *Client) SetDisplayName(ctx context.Context, displayName string) error {
	path := fmt.Sprintf("/_matrix/client/v3/profile/%s/displayname", url.PathEscape(c.botUserID))

	resp, err := c.doRequest(ctx, http.MethodPut, path, nil, map[string]interface{}{
		"displayname": displayName,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set displayname failed: %s - %s", resp.Status, string(body))
	}

	return nil
}

// GetBotUserID returns the bot's user ID
func (c *Client) GetBotUserID() string {
	return c.botUserID
}
