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
	leaveGrace time.Duration

	// local map of channel -> clients matching that channel, to distribute Redis messages locally
	mu            sync.RWMutex
	subscriptions map[string]map[*ws.Client]bool
	// clientChannelTTLs stores per-client per-channel TTL overrides from subscribe frames.
	clientChannelTTLs map[*ws.Client]map[string]time.Duration
	// clientChannelMetadata stores per-client per-channel metadata from subscribe frames.
	clientChannelMetadata map[*ws.Client]map[string]json.RawMessage

	// leaveTimers holds pending presence.left grace-period timers keyed by "channel\x00userID".
	leaveMu     sync.Mutex
	leaveTimers map[string]*time.Timer
}

func NewDefaultRouter(p pubsub.Engine, pr *presence.Tracker, g *ws.Gateway, defaultTTL, leaveGrace time.Duration) *DefaultRouter {
	return &DefaultRouter{
		pubsub:                p,
		presence:              pr,
		gateway:               g,
		metrics:               &RouterMetrics{},
		defaultTTL:            defaultTTL,
		leaveGrace:            leaveGrace,
		subscriptions:         make(map[string]map[*ws.Client]bool),
		clientChannelTTLs:     make(map[*ws.Client]map[string]time.Duration),
		clientChannelMetadata: make(map[*ws.Client]map[string]json.RawMessage),
		leaveTimers:           make(map[string]*time.Timer),
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
	delete(r.clientChannelMetadata, client)

	for channel, clients := range r.subscriptions {
		if _, ok := clients[client]; ok {
			delete(clients, client)

			// Schedule presence.left after the grace period.
			// If the user reconnects within the window, the timer is cancelled
			// and presence.left is suppressed entirely.
			// presence.Remove is deferred to the same moment so the user
			// remains visible in presence during the grace window.
			r.scheduleLeave(channel, client.UserID)

			// Unsubscribe from Redis if no local clients remain.
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

	// Case 4: idempotent subscribe — if this connection is already subscribed, no-op.
	if subs, exists := r.subscriptions[channel]; exists && subs[client] {
		return
	}

	// Cancel any pending presence.left grace timer for this user.
	// This handles the rapid reconnect case: the new client (different *ws.Client)
	// arrives before the grace window expires, suppressing the presence.left event.
	r.cancelLeaveTimer(channel, client.UserID)

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

	// Validate and store metadata if provided (max 2 KB).
	const maxMetadataBytes = 2048
	var metadata json.RawMessage
	if len(msg.Metadata) > 0 {
		if len(msg.Metadata) > maxMetadataBytes {
			client.SendJSON(core.Envelope{Type: "error", Payload: json.RawMessage(`{"error":"metadata exceeds 2 KB limit"}`)})
			// Proceed with subscription but without metadata.
		} else {
			metadata = msg.Metadata
			r.presence.SetMetadata(ctx, channel, client.UserID, ttl, metadata)
			if _, ok := r.clientChannelMetadata[client]; !ok {
				r.clientChannelMetadata[client] = make(map[string]json.RawMessage)
			}
			r.clientChannelMetadata[client][channel] = metadata
		}
	}

	// Case 2: check whether the user is already present in Redis before heartbeating.
	// If yes, this is a reconnect within the TTL window → emit presence.rejoined
	// instead of presence.joined so subscribers don't briefly see the user as offline.
	alreadyPresent, _ := r.presence.IsPresent(ctx, channel, client.UserID)

	r.presence.Heartbeat(ctx, channel, client.UserID, ttl)

	event := "presence.joined"
	if alreadyPresent {
		event = "presence.rejoined"
	}

	joinedPayload, _ := json.Marshal(map[string]interface{}{
		"user":     client.UserID,
		"metadata": metadataOrEmpty(metadata),
	})
	b, _ := json.Marshal(core.Envelope{
		Type:    core.TypeMessage,
		Channel: channel,
		Event:   event,
		r.presence.RefreshMetadata(ctx, msg.Channel, client.UserID, ttl)
	}
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

// hasMetadata reports whether client has stored metadata for channel.
func (r *DefaultRouter) hasMetadata(client *ws.Client, channel string) bool {
	if m, ok := r.clientChannelMetadata[client]; ok {
		_, ok := m[channel]
		return ok
	}
	return false
}

// metadataOrEmpty returns meta if non-nil, otherwise an empty JSON object.
func metadataOrEmpty(meta json.RawMessage) json.RawMessage {
	if meta != nil {
		return meta
	}
	return json.RawMessage(`{}`)
}

// leaveKey returns the map key used for grace-period leave timers.
func leaveKey(channel, userID string) string {
	return channel + "\x00" + userID
}

// scheduleLeave defers the presence.left event and Redis removal by leaveGrace.
// If the same userID re-subscribes to the channel before the timer fires,
// cancelLeaveTimer suppresses the event entirely.
// Must be called with r.mu held.
func (r *DefaultRouter) scheduleLeave(channel, userID string) {
	if r.leaveGrace <= 0 {
		// No grace period — fire immediately.
		go r.fireLeave(channel, userID)
		return
	}
	key := leaveKey(channel, userID)
	r.leaveMu.Lock()
	if t, ok := r.leaveTimers[key]; ok {
		t.Stop()
	}
	r.leaveTimers[key] = time.AfterFunc(r.leaveGrace, func() {
		r.leaveMu.Lock()
		delete(r.leaveTimers, key)
		r.leaveMu.Unlock()
		r.fireLeave(channel, userID)
	})
	r.leaveMu.Unlock()
}

// cancelLeaveTimer cancels a pending grace-period leave for userID on channel.
// Must be called with r.mu held.
func (r *DefaultRouter) cancelLeaveTimer(channel, userID string) {
	key := leaveKey(channel, userID)
	r.leaveMu.Lock()
	if t, ok := r.leaveTimers[key]; ok {
		t.Stop()
		delete(r.leaveTimers, key)
	}
	r.leaveMu.Unlock()
}

// fireLeave removes presence state and broadcasts presence.left.
func (r *DefaultRouter) fireLeave(channel, userID string) {
	ctx := context.Background()
	r.presence.Remove(ctx, channel, userID)
	r.presence.RemoveMetadata(ctx, channel, userID)
	b, _ := json.Marshal(core.Envelope{
		Type:    core.TypeMessage,
		Channel: channel,
		Event:   "presence.left",
		Payload: json.RawMessage(`{"user":"` + userID + `"}`),
	})
	r.pubsub.Publish(ctx, channel, b)
}

// updatePresence is called on core.TypePing to extend TTL of all current channels.
func (r *DefaultRouter) updatePresence(ctx context.Context, client *ws.Client) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for channel, clients := range r.subscriptions {
		if _, ok := clients[client]; ok {
			ttl := r.resolveTTL(client, channel)
			r.presence.Heartbeat(ctx, channel, client.UserID, ttl)
			if r.hasMetadata(client, channel) {
				r.presence.RefreshMetadata(ctx, channel, client.UserID, ttl)
			}
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
