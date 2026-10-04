package core

import (
	"encoding/json"
	"strings"
)

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
	// Sender is the authenticated userID of the connection that published the
	// message. It is stamped by the server on client publishes (any
	// client-supplied value is discarded) and absent on REST/server publishes
	// and server-generated events.
	Sender string `json:"sender,omitempty"`
}

// reservedEventPrefixes are event namespaces only the server may emit.
var reservedEventPrefixes = []string{"presence.", "orbit."}

// IsReservedEvent reports whether event is in a server-only namespace
// ("presence.*" or "orbit.*"). Clients may not publish such events.
func IsReservedEvent(event string) bool {
	for _, p := range reservedEventPrefixes {
		if strings.HasPrefix(event, p) {
			return true
		}
	}
	return false
}
