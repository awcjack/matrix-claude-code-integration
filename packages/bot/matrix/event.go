package matrix

// Event represents a Matrix event
type Event struct {
	EventID        string                 `json:"event_id"`
	RoomID         string                 `json:"room_id"`
	Sender         string                 `json:"sender"`
	Type           string                 `json:"type"`
	StateKey       *string                `json:"state_key,omitempty"`
	Content        map[string]interface{} `json:"content"`
	OriginServerTS int64                  `json:"origin_server_ts"`
}
