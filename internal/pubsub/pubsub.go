package pubsub

import "context"

// MessageHandler is the callback for processing messages received from the pubsub engine.
type MessageHandler func(channel string, payload []byte)

// Engine defines the interface for distributed publish/subscribe systems (Redis, NATS, etc.).
type Engine interface {
	Publish(ctx context.Context, channel string, payload []byte) error
	Subscribe(ctx context.Context, channel string, handler MessageHandler) error
	Unsubscribe(ctx context.Context, channel string) error
	Close() error
}
