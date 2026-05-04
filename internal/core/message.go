package core

import "encoding/json"

type MessageType string

const (
	TypeSubscribe   MessageType = "subscribe"
	TypeUnsubscribe MessageType = "unsubscribe"
	TypePublish     MessageType = "publish"
	TypeMessage     MessageType = "message"
	TypePing        MessageType = "ping"
	TypePong        MessageType = "pong"
)

// Envelope is the standard JSON frame for all WebSocket communication in Orbit.
type Envelope struct {
	Type    MessageType     `json:"type"`
	Channel string          `json:"channel,omitempty"`
	Event   string          `json:"event,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// TTL is an optional presence TTL in seconds, valid only on subscribe frames.
	// Accepted range: 5–3600. Falls back to the server default when zero or absent.
	TTL int `json:"ttl,omitempty"`
	// Metadata is an optional opaque JSON object, valid only on subscribe frames.
	// Max 2 KB serialised. Stored alongside the presence entry and forwarded in
	// presence.joined events.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}
