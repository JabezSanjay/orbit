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
}
