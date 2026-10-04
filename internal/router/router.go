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
	"github.com/orbit/orbit/internal/pubsub"
	"github.com/orbit/orbit/internal/ws"
)

type RouterMetrics struct {
	MessagesPublished int64
}

// PresenceStore is the subset of presence.Tracker the router depends on.
// Defined here (consumer side) so tests can substitute an in-memory fake.
type PresenceStore interface {
	Heartbeat(ctx context.Context, channel, userID string, ttl time.Duration) error
	Remove(ctx context.Context, channel, userID string) error
	IsPresent(ctx context.Context, channel, userID string) (bool, error)
	SetMetadata(ctx context.Context, channel, userID string, ttl time.Duration, metadata json.RawMessage) error
	RefreshMetadata(ctx context.Context, channel, userID string, ttl time.Duration)
	RemoveMetadata(ctx context.Context, channel, userID string)
}

// subscription is one client's subscription to one channel.
type subscription struct {
	ttl      time.Duration   // presence TTL for this client+channel
	metadata json.RawMessage // nil when the subscribe frame carried none
}

// channelSub is a lock-free snapshot of one of a client's subscriptions, used
// to do presence I/O after r.mu has been released.
type channelSub struct {
	channel string
	subscription
}

// DefaultRouter routes client frames to the pubsub engine and fans engine
// messages out to local clients.
//
// Locking rules:
//   - r.mu guards the in-memory routing tables (subscriptions, clientSubs).
//     It is NEVER held across Redis I/O (publish, presence, engine
//     subscribe/unsubscribe). Fanout workers take r.mu.RLock and only call the
//     non-blocking Client.SendJSON while holding it.
//   - r.engineMu serialises engine Subscribe/Unsubscribe calls; see syncEngine.
//   - r.leaveMu guards leaveTimers and may be taken while r.mu is held.
//
// Lock order: engineMu -> mu -> leaveMu. Nothing that holds mu acquires engineMu.
type DefaultRouter struct {
	pubsub     pubsub.Engine
	presence   PresenceStore
	gateway    *ws.Gateway
	metrics    *RouterMetrics
	defaultTTL time.Duration
	leaveGrace time.Duration

	mu sync.RWMutex
	// subscriptions is the fanout index: channel -> local clients.
	// A channel key exists iff it has at least one local client.
	subscriptions map[string]map[*ws.Client]struct{}
	// clientSubs is the reverse index: client -> channel -> subscription.
	// A client key exists iff it has at least one subscription.
	clientSubs map[*ws.Client]map[string]*subscription

	// engineSubs records which channels the engine is currently subscribed to.
	// Guarded by engineMu, not mu.
	engineMu   sync.Mutex
	engineSubs map[string]bool

	// leaveTimers holds pending presence.left grace-period timers keyed by "channel\x00userID".
	leaveMu     sync.Mutex
	leaveTimers map[string]*time.Timer
}

func NewDefaultRouter(p pubsub.Engine, pr PresenceStore, g *ws.Gateway, defaultTTL, leaveGrace time.Duration) *DefaultRouter {
	return &DefaultRouter{
		pubsub:        p,
		presence:      pr,
		gateway:       g,
		metrics:       &RouterMetrics{},
		defaultTTL:    defaultTTL,
		leaveGrace:    leaveGrace,
		subscriptions: make(map[string]map[*ws.Client]struct{}),
		clientSubs:    make(map[*ws.Client]map[string]*subscription),
		engineSubs:    make(map[string]bool),
		leaveTimers:   make(map[string]*time.Timer),
	}
}

func (r *DefaultRouter) GetMetrics() *RouterMetrics {
	return r.metrics
}

// ---------------------------------------------------------------------------
// In-memory routing table primitives. All *Locked methods require r.mu held
// for writing and do no I/O.
// ---------------------------------------------------------------------------

// addSubscriptionLocked records client as subscribed to channel.
// It returns false (and changes nothing) if the client is already subscribed.
func (r *DefaultRouter) addSubscriptionLocked(client *ws.Client, channel string, sub *subscription) bool {
	if _, ok := r.clientSubs[client][channel]; ok {
		return false
	}
	clients, ok := r.subscriptions[channel]
	if !ok {
		clients = make(map[*ws.Client]struct{})
		r.subscriptions[channel] = clients
	}
	clients[client] = struct{}{}

	subs, ok := r.clientSubs[client]
	if !ok {
		subs = make(map[string]*subscription)
		r.clientSubs[client] = subs
	}
	subs[channel] = sub
	return true
}

// removeSubscriptionLocked removes client from channel. removed reports whether
// the client was subscribed; emptied reports whether channel has no local
// clients left (the caller must then syncEngine(channel) after unlocking).
func (r *DefaultRouter) removeSubscriptionLocked(client *ws.Client, channel string) (removed, emptied bool) {
	subs, ok := r.clientSubs[client]
	if !ok {
		return false, false
	}
	if _, ok := subs[channel]; !ok {
		return false, false
	}
	delete(subs, channel)
	if len(subs) == 0 {
		delete(r.clientSubs, client)
	}

	clients := r.subscriptions[channel]
	delete(clients, client)
	if len(clients) == 0 {
		delete(r.subscriptions, channel)
		return true, true
	}
	return true, false
}

// clientChannelsLocked returns the channels client is subscribed to.
// Requires at least r.mu.RLock.
func (r *DefaultRouter) clientChannelsLocked(client *ws.Client) []string {
	subs := r.clientSubs[client]
	out := make([]string, 0, len(subs))
	for ch := range subs {
		out = append(out, ch)
	}
	return out
}

// clientSubscriptions returns a snapshot of client's subscriptions, safe to
// use after the lock is released.
func (r *DefaultRouter) clientSubscriptions(client *ws.Client) []channelSub {
	r.mu.RLock()
	defer r.mu.RUnlock()
	subs := r.clientSubs[client]
	out := make([]channelSub, 0, len(subs))
	for ch, s := range subs {
		out = append(out, channelSub{channel: ch, subscription: *s})
	}
	return out
}

// lookupSubscription returns a copy of client's subscription to channel.
func (r *DefaultRouter) lookupSubscription(client *ws.Client, channel string) (subscription, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.clientSubs[client][channel]
	if !ok {
		return subscription{}, false
	}
	return *s, true
}

// syncEngine reconciles the engine's subscription state for each channel with
// the in-memory routing table: subscribed iff the channel has local clients.
//
// Why this is race-free without holding r.mu across Redis I/O: every mutation
// that can change "has local clients" (first client in, last client out) calls
// syncEngine AFTER releasing r.mu. syncEngine is serialised by engineMu and
// reads the CURRENT table state (not the state the caller saw), so whichever
// sync runs last for a channel always observes the final state and leaves the
// engine matching it. E.g. "last client leaves" racing "new client joins":
// both call syncEngine; whichever runs second sees the channel non-empty and
// ensures the engine is subscribed, so the channel is never left unsubscribed
// with a local client.
//
// handleSubscribe calls syncEngine unconditionally (not only on 0->1) so it
// cannot return — and publish presence.joined — before the engine
// subscription created by a concurrent first subscriber is in place.
func (r *DefaultRouter) syncEngine(channels ...string) {
	if len(channels) == 0 {
		return
	}
	r.engineMu.Lock()
	defer r.engineMu.Unlock()

	for _, channel := range channels {
		r.mu.RLock()
		want := len(r.subscriptions[channel]) > 0
		r.mu.RUnlock()

		have := r.engineSubs[channel]
		switch {
		case want && !have:
			if err := r.pubsub.Subscribe(context.Background(), channel, r.handleRedisMessage); err != nil {
				log.Printf("Redis subscribe error on %s: %v", channel, err)
				continue
			}
			r.engineSubs[channel] = true
		case !want && have:
			if err := r.pubsub.Unsubscribe(context.Background(), channel); err != nil {
				log.Printf("Redis unsubscribe error on %s: %v", channel, err)
			}
			delete(r.engineSubs, channel)
		}
	}
}

// ---------------------------------------------------------------------------
// ws.Router implementation
// ---------------------------------------------------------------------------

// HandleDisconnect removes every subscription held by client. It only touches
// the client's own channels (via the reverse index) and performs Redis I/O
// after releasing r.mu.
func (r *DefaultRouter) HandleDisconnect(ctx context.Context, client *ws.Client) {
	var emptied []string

	r.mu.Lock()
	for _, channel := range r.clientChannelsLocked(client) {
		_, isEmpty := r.removeSubscriptionLocked(client, channel)
		if isEmpty {
			emptied = append(emptied, channel)
		}
		// Schedule presence.left after the grace period. If the user
		// re-subscribes within the window the timer is cancelled and
		// presence.left is suppressed. presence.Remove is deferred to the same
		// moment so the user stays visible during the grace window.
		// Scheduled under r.mu so it is ordered against cancelLeaveTimer in
		// handleSubscribe.
		r.scheduleLeave(channel, client.UserID)
	}
	r.mu.Unlock()

	// Unsubscribe the node from channels with no local clients left.
	r.syncEngine(emptied...)
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

	// Validate metadata (max 2 KB). Oversized metadata is reported but the
	// subscription proceeds without it.
	const maxMetadataBytes = 2048
	var metadata json.RawMessage
	if len(msg.Metadata) > 0 {
		if len(msg.Metadata) > maxMetadataBytes {
			client.SendJSON(core.Envelope{Type: "error", Payload: json.RawMessage(`{"error":"metadata exceeds 2 KB limit"}`)})
		} else {
			metadata = msg.Metadata
		}
	}

	r.mu.Lock()
	// Idempotent subscribe — if this connection is already subscribed, no-op.
	added := r.addSubscriptionLocked(client, channel, &subscription{ttl: ttl, metadata: metadata})
	if added {
		// Cancel any pending presence.left grace timer for this user. This
		// handles the rapid reconnect case: the new connection arrives before
		// the grace window expires, suppressing the presence.left event.
		r.cancelLeaveTimer(channel, client.UserID)
	}
	r.mu.Unlock()
	if !added {
		return
	}

	// Ensure the node is subscribed in Redis before announcing the join, so
	// local subscribers (including this client) receive presence.joined.
	r.syncEngine(channel)

	if metadata != nil {
		r.presence.SetMetadata(ctx, channel, client.UserID, ttl, metadata)
	}

	// If the user is already present in Redis this is a reconnect within the
	// TTL window → emit presence.rejoined instead of presence.joined so
	// subscribers don't briefly see the user as offline.
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
		Payload: joinedPayload,
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

	// Publishing counts as a presence heartbeat — but only on channels this
	// connection is subscribed to, so publish-only clients never appear in
	// presence (they would have no disconnect path to remove them).
	if sub, ok := r.lookupSubscription(client, msg.Channel); ok {
		r.heartbeat(ctx, client.UserID, channelSub{channel: msg.Channel, subscription: sub})
	}
}

// updatePresence is called on core.TypePing to extend TTL of all current channels.
func (r *DefaultRouter) updatePresence(ctx context.Context, client *ws.Client) {
	for _, s := range r.clientSubscriptions(client) {
		r.heartbeat(ctx, client.UserID, s)
	}
}

// heartbeat refreshes userID's presence (and metadata expiry) for one subscription.
func (r *DefaultRouter) heartbeat(ctx context.Context, userID string, s channelSub) {
	r.presence.Heartbeat(ctx, s.channel, userID, s.ttl)
	if s.metadata != nil {
		r.presence.RefreshMetadata(ctx, s.channel, userID, s.ttl)
	}
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
// Must be called with r.mu held. Does no I/O itself.
func (r *DefaultRouter) scheduleLeave(channel, userID string) {
	if r.leaveGrace <= 0 {
		// No grace period — fire immediately (off the lock).
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

// handleRedisMessage is the engine callback: it fans a channel message out to
// every local subscriber. It runs on engine fanout workers; SendJSON is
// non-blocking, so holding r.mu.RLock while iterating is cheap.
func (r *DefaultRouter) handleRedisMessage(channel string, payload []byte) {
	var env core.Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return
	}

	// Convert publish to message for downstream clients
	if env.Type == core.TypePublish {
		env.Type = core.TypeMessage
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for client := range r.subscriptions[channel] {
		client.SendJSON(env)
	}
}
