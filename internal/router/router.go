package router

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/orbit/orbit/internal/auth"
	"github.com/orbit/orbit/internal/core"
	"github.com/orbit/orbit/internal/presence"
	"github.com/orbit/orbit/internal/pubsub"
	"github.com/orbit/orbit/internal/ws"
)

type RouterMetrics struct {
	MessagesPublished int64
}

type DefaultRouter struct {
	pubsub     pubsub.Engine
	presence   *presence.Tracker
	gateway    *ws.Gateway
	metrics    *RouterMetrics
	defaultTTL time.Duration

	// local map of channel -> clients matching that channel, to distribute Redis messages locally
	mu            sync.RWMutex
	subscriptions map[string]map[*ws.Client]bool
	// clientChannelTTLs stores per-client per-channel TTL overrides from subscribe frames.
	clientChannelTTLs map[*ws.Client]map[string]time.Duration
}

func NewDefaultRouter(p pubsub.Engine, pr *presence.Tracker, g *ws.Gateway, defaultTTL time.Duration) *DefaultRouter {
	return &DefaultRouter{
		pubsub:            p,
		presence:          pr,
		gateway:           g,
		metrics:           &RouterMetrics{},
		defaultTTL:        defaultTTL,
		subscriptions:     make(map[string]map[*ws.Client]bool),
		clientChannelTTLs: make(map[*ws.Client]map[string]time.Duration),
	}
}

func (r *DefaultRouter) GetMetrics() *RouterMetrics {
	return r.metrics
}

// HandleDisconnect cleans up a user's presence state when WS drops.
func (r *DefaultRouter) HandleDisconnect(ctx context.Context, client *ws.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.clientChannelTTLs, client)

	for channel, clients := range r.subscriptions {
		if _, ok := clients[client]; ok {
			delete(clients, client)
			r.presence.Remove(context.Background(), channel, client.UserID)
			
			// Broadcast presence.left
			b, _ := json.Marshal(core.Envelope{
				Type:    core.TypeMessage,
				Channel: channel,
				Event:   "presence.left",
				Payload: json.RawMessage(`{"user":"` + client.UserID + `"}`),
			})
			r.pubsub.Publish(context.Background(), channel, b)

			// Unsubscribe from Redis if this was the last client locally
			if len(clients) == 0 {
				r.pubsub.Unsubscribe(context.Background(), channel)
				delete(r.subscriptions, channel)
			}
		}
	}
}

func (r *DefaultRouter) HandleMessage(ctx context.Context, client *ws.Client, msg core.Envelope) {
	switch msg.Type {
	case core.TypeSubscribe:
		r.handleSubscribe(ctx, client, msg)
	case core.TypePublish:
		r.handlePublish(ctx, client, msg)
	case core.TypePing:
		// Heartbeats handled via presence updates for all subs
		r.updatePresence(ctx, client)
	default:
		log.Printf("Unknown message type: %s", msg.Type)
	}
}

func (r *DefaultRouter) handleSubscribe(ctx context.Context, client *ws.Client, msg core.Envelope) {
	channel := msg.Channel
	if channel == "" {
		return
	}

	if !auth.CanSubscribe(client.Permissions, channel) {
		client.SendJSON(core.Envelope{Type: "error", Payload: json.RawMessage(`{"error":"not authorized to subscribe to this channel"}`)})
		return
	}

	// Resolve TTL: use per-channel override from the subscribe frame if provided.
	ttl := r.defaultTTL
	if msg.TTL != 0 {
		if msg.TTL < 5 || msg.TTL > 3600 {
			client.SendJSON(core.Envelope{Type: "error", Payload: json.RawMessage(`{"error":"ttl must be between 5 and 3600 seconds"}`)})
			return
		}
		ttl = time.Duration(msg.TTL) * time.Second
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.subscriptions[channel]; !exists {
		r.subscriptions[channel] = make(map[*ws.Client]bool)

		// Map redis pubsub into our local gateway fanout
		err := r.pubsub.Subscribe(context.Background(), channel, r.handleRedisMessage)
		if err != nil {
			log.Printf("Redis subscribe error: %v", err)
		}
	}

	r.subscriptions[channel][client] = true

	// Store per-channel TTL override for this client.
	if _, ok := r.clientChannelTTLs[client]; !ok {
		r.clientChannelTTLs[client] = make(map[string]time.Duration)
	}
	r.clientChannelTTLs[client][channel] = ttl

	r.presence.Heartbeat(ctx, channel, client.UserID, ttl)

	// Broadcast presence.joined
	b, _ := json.Marshal(core.Envelope{
		Type:    core.TypeMessage,
		Channel: channel,
		Event:   "presence.joined",
		Payload: json.RawMessage(`{"user":"` + client.UserID + `"}`),
	})
	r.pubsub.Publish(ctx, channel, b)
}

func (r *DefaultRouter) handlePublish(ctx context.Context, client *ws.Client, msg core.Envelope) {
	if msg.Channel == "" {
		return
	}

	if !auth.CanPublish(client.Permissions, msg.Channel) {
		client.SendJSON(core.Envelope{Type: "error", Payload: json.RawMessage(`{"error":"not authorized to publish to this channel"}`)})
		return
	}

	// Fan out the message via Redis
	b, _ := json.Marshal(msg)
	r.pubsub.Publish(ctx, msg.Channel, b)
	atomic.AddInt64(&r.metrics.MessagesPublished, 1)

	// Update presence — publishing counts as a heartbeat.
	r.presence.Heartbeat(ctx, msg.Channel, client.UserID, r.resolveTTL(client, msg.Channel))
}

// resolveTTL returns the per-channel TTL override for client, falling back to the server default.
// Callers must hold at least r.mu.RLock.
func (r *DefaultRouter) resolveTTL(client *ws.Client, channel string) time.Duration {
	if m, ok := r.clientChannelTTLs[client]; ok {
		if ttl, ok := m[channel]; ok {
			return ttl
		}
	}
	return r.defaultTTL
}

// updatePresence is called on core.TypePing to extend TTL of all current channels.
func (r *DefaultRouter) updatePresence(ctx context.Context, client *ws.Client) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for channel, clients := range r.subscriptions {
		if _, ok := clients[client]; ok {
			r.presence.Heartbeat(ctx, channel, client.UserID, r.resolveTTL(client, channel))
		}
	}
}

func (r *DefaultRouter) handleRedisMessage(channel string, payload []byte) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clients, exists := r.subscriptions[channel]
	if !exists {
		return
	}

	var env core.Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return
	}
	
	// Convert publish to message for downstream clients
	if env.Type == core.TypePublish {
		env.Type = core.TypeMessage
	}

	for client := range clients {
		client.SendJSON(env)
	}
}
