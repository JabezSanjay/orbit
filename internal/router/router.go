package router

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JabezSanjay/orbit/internal/auth"
	"github.com/JabezSanjay/orbit/internal/core"
	"github.com/JabezSanjay/orbit/internal/presence"
	"github.com/JabezSanjay/orbit/internal/pubsub"
	"github.com/JabezSanjay/orbit/internal/ws"
)

type RouterMetrics struct {
	MessagesPublished int64
}

// PresenceStore is the subset of presence.Tracker the router depends on.
// Defined here (consumer side) so tests can substitute an in-memory fake.
//
// Presence is refcounted per connection, cluster-wide: Join/Leave/Finalize
// are atomic in the store, and the store itself publishes the presence event
// that a state change produces, so events reach the channel in the same order
// as the changes (see presence.Tracker).
type PresenceStore interface {
	// Join registers connID and publishes joined/rejoined on the user's 0 -> 1
	// transition (or updated when a later connection carries metadata).
	Join(ctx context.Context, channel, userID, connID string, ttl time.Duration, metadata json.RawMessage, ev presence.JoinEvents) (presence.JoinResult, error)
	// Heartbeat refreshes connID's entry and the user's presence/metadata expiry.
	Heartbeat(ctx context.Context, channel, userID, connID string, ttl time.Duration) error
	// Leave removes connID and returns the user's remaining live connections
	// in channel across all nodes.
	Leave(ctx context.Context, channel, userID, connID string) (int64, error)
	// Finalize removes the user and publishes leftMsg iff they have no live
	// connection on any node. Reports whether left was published.
	Finalize(ctx context.Context, channel, userID string, leftMsg []byte) (bool, error)
	// UpdateMetadata replaces the user's metadata (nil clears it), refreshes
	// connID like a heartbeat and publishes updatedMsg, atomically.
	UpdateMetadata(ctx context.Context, channel, userID, connID string, ttl time.Duration, metadata json.RawMessage, updatedMsg []byte) error
	// GetUsersWithMetadata lists the channel's present users (the same data
	// as GET /api/presence).
	GetUsersWithMetadata(ctx context.Context, channel string) ([]presence.PresenceEntry, error)
}

// subscription is one client's subscription to one channel.
type subscription struct {
	ttl time.Duration // presence TTL for this client+channel
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
//
// Presence correctness does not depend on these locks: the store's atomic
// Join/Leave/Finalize decide every user-level transition cluster-wide, and
// leave timers are only a delay before Finalize (cancelling one on a local
// resubscribe merely saves a Redis round trip).
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
	channels := r.clientChannelsLocked(client)
	for _, channel := range channels {
		if _, isEmpty := r.removeSubscriptionLocked(client, channel); isEmpty {
			emptied = append(emptied, channel)
		}
	}
	r.mu.Unlock()

	// Unsubscribe the node from channels with no local clients left.
	r.syncEngine(emptied...)

	// A disconnect may be unintended (network blip, node restart), so the
	// user-level leave waits out the grace period.
	for _, channel := range channels {
		r.leave(ctx, channel, client, false)
	}
}

// leave removes client's connection from channel's presence. When it was the
// user's last live connection on any node, presence.left follows — right away
// for an explicit unsubscribe, after leaveGrace for a disconnect. Either way
// Finalize re-checks the cluster-wide count atomically before publishing, so
// a reconnect anywhere in the meantime suppresses the left.
func (r *DefaultRouter) leave(ctx context.Context, channel string, client *ws.Client, immediate bool) {
	remaining, err := r.presence.Leave(ctx, channel, client.UserID, client.ID)
	if err != nil {
		// The connection entry ages out after its TTL instead.
		log.Printf("presence leave error on %s: %v", channel, err)
		return
	}
	if remaining > 0 {
		return // the user is still connected elsewhere: no event
	}
	if immediate {
		r.cancelLeaveTimer(channel, client.UserID)
		r.fireLeave(channel, client.UserID)
		return
	}
	r.scheduleLeave(channel, client.UserID)
}

func (r *DefaultRouter) HandleMessage(ctx context.Context, client *ws.Client, msg core.Envelope) {
	switch msg.Type {
	case core.TypeSubscribe:
		r.handleSubscribe(ctx, client, msg)
	case core.TypeUnsubscribe:
		r.handleUnsubscribe(ctx, client, msg)
	case core.TypePublish:
		r.handlePublish(ctx, client, msg)
	case core.TypePresenceUpdate:
		r.handlePresenceUpdate(ctx, client, msg)
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
		client.SendJSON(errorEnvelope("not authorized to subscribe to this channel"))
		return
	}

	// Resolve TTL: use per-channel override from the subscribe frame if provided.
	ttl := r.defaultTTL
	if msg.TTL != 0 {
		if msg.TTL < 5 || msg.TTL > 3600 {
			client.SendJSON(errorEnvelope("ttl must be between 5 and 3600 seconds"))
			return
		}
		ttl = time.Duration(msg.TTL) * time.Second
	}

	// Validate metadata (max 2 KB). Oversized metadata is reported but the
	// subscription proceeds without it.
	var metadata json.RawMessage
	if len(msg.Metadata) > 0 && string(msg.Metadata) != "null" {
		if len(msg.Metadata) > maxMetadataBytes {
			client.SendJSON(errorEnvelope("metadata exceeds 2 KB limit"))
		} else {
			metadata = msg.Metadata
		}
	}

	r.mu.Lock()
	// Idempotent subscribe — if this connection is already subscribed, no-op.
	added := r.addSubscriptionLocked(client, channel, &subscription{ttl: ttl})
	if added {
		// Cancel any pending presence.left grace timer for this user. This
		// handles the rapid reconnect case: the new connection arrives before
		// the grace window expires, suppressing the presence.left event. (A
		// timer that already fired is harmless: Finalize sees this connection.)
		r.cancelLeaveTimer(channel, client.UserID)
	}
	r.mu.Unlock()
	if !added {
		return
	}

	// Ensure the node is subscribed in Redis before announcing the join, so
	// local subscribers (including this client) receive presence.joined.
	r.syncEngine(channel)

	// Register this connection. The store publishes, atomically with the
	// refcount change:
	//   - presence.joined   on the user's first connection (0 -> 1, cluster-wide)
	//   - presence.rejoined on 0 -> 1 while the user is still listed as present
	//     (reconnect inside the leave grace window / TTL), so subscribers don't
	//     briefly see the user as offline
	//   - presence.updated  when a further connection carries metadata (last
	//     write wins per user)
	//   - nothing           for a further connection without metadata
	payload := presencePayload(client.UserID, metadata)
	ev := presence.JoinEvents{
		Joined:   presenceEvent(channel, "presence.joined", payload),
		Rejoined: presenceEvent(channel, "presence.rejoined", payload),
		Updated:  presenceEvent(channel, "presence.updated", payload),
	}
	if _, err := r.presence.Join(ctx, channel, client.UserID, client.ID, ttl, metadata, ev); err != nil {
		log.Printf("presence join error on %s: %v", channel, err)
	}

	r.sendPresenceState(ctx, client, channel)
}

// sendPresenceState sends client — and only client — a presence.state
// snapshot of channel: {"users":[{"user":..,"metadata":{..}}]}, the same
// shape as GET /api/presence.
//
// Ordering: the snapshot is read after the subscription is active (local
// routing table + Redis subscription) and after this connection's own Join,
// so it includes the subscriber, and every presence change committed after
// the read is also delivered to the client as an event. Events committed
// shortly before the read may arrive before or after the snapshot, so clients
// should apply every presence event received since subscribing on top of the
// snapshot, per user, last event wins (the JS SDK's presence helper does).
// Presence events are published atomically with the state they describe, so
// a user's last event always matches the current state.
func (r *DefaultRouter) sendPresenceState(ctx context.Context, client *ws.Client, channel string) {
	users, err := r.presence.GetUsersWithMetadata(ctx, channel)
	if err != nil {
		log.Printf("presence snapshot error on %s: %v", channel, err)
		return
	}
	if users == nil {
		users = []presence.PresenceEntry{}
	}
	payload, _ := json.Marshal(map[string]interface{}{"users": users})
	client.SendJSON(core.Envelope{
		Type:    core.TypeMessage,
		Channel: channel,
		Event:   "presence.state",
		Payload: payload,
	})
}

// handleUnsubscribe removes client's subscription to msg.Channel. Unknown,
// empty or repeated unsubscribes are silent no-ops; no ACL check is needed to
// leave a channel and no acknowledgement frame is sent.
//
// An explicit unsubscribe is a deliberate leave, so when it drops the user's
// last connection (cluster-wide) presence.left is published immediately
// instead of after the ORBIT_PRESENCE_LEAVE_GRACE_MS window (the grace period
// exists to absorb reconnects after an unintended disconnect). If the user is
// still subscribed on another connection, nothing is published.
func (r *DefaultRouter) handleUnsubscribe(ctx context.Context, client *ws.Client, msg core.Envelope) {
	channel := msg.Channel
	if channel == "" {
		return
	}

	r.mu.Lock()
	removed, emptied := r.removeSubscriptionLocked(client, channel)
	r.mu.Unlock()
	if !removed {
		return
	}

	if emptied {
		r.syncEngine(channel)
	}
	r.leave(ctx, channel, client, true)
}

// maxMetadataBytes caps presence metadata on subscribe and presence_update.
const maxMetadataBytes = 2048

// handlePresenceUpdate handles a presence_update frame: it replaces the
// user's metadata on a channel this connection is subscribed to and
// broadcasts presence.updated {user, metadata} to every subscriber on every
// node. A missing/null metadata or {} clears it. It refreshes presence like a
// heartbeat and never emits joined/left. Metadata is per user, so updates
// from a user's different connections overwrite each other (last write wins).
// Frames count against the per-connection inbound rate limit
// (ORBIT_CLIENT_MSGS_PER_SEC) like every other frame.
func (r *DefaultRouter) handlePresenceUpdate(ctx context.Context, client *ws.Client, msg core.Envelope) {
	channel := msg.Channel
	sub, ok := r.lookupSubscription(client, channel)
	if !ok {
		client.SendJSON(errorEnvelope("not subscribed to this channel"))
		return
	}
	if !auth.CanSubscribe(client.Permissions, channel) {
		client.SendJSON(errorEnvelope("not authorized to subscribe to this channel"))
		return
	}
	if len(msg.Metadata) > maxMetadataBytes {
		client.SendJSON(errorEnvelope("metadata exceeds 2 KB limit"))
		return
	}

	var metadata json.RawMessage
	var compact bytes.Buffer
	if len(msg.Metadata) > 0 && json.Compact(&compact, msg.Metadata) == nil {
		if s := compact.String(); s != "null" && s != "{}" {
			metadata = msg.Metadata
		}
	}

	updated := presenceEvent(channel, "presence.updated", presencePayload(client.UserID, metadata))
	if err := r.presence.UpdateMetadata(ctx, channel, client.UserID, client.ID, sub.ttl, metadata, updated); err != nil {
		log.Printf("presence update error on %s: %v", channel, err)
		client.SendJSON(errorEnvelope("presence update failed"))
	}
}

func (r *DefaultRouter) handlePublish(ctx context.Context, client *ws.Client, msg core.Envelope) {
	if msg.Channel == "" {
		return
	}

	if !auth.CanPublish(client.Permissions, msg.Channel) {
		client.SendJSON(errorEnvelope("not authorized to publish to this channel"))
		return
	}

	if core.IsReservedEvent(msg.Event) {
		client.SendJSON(errorEnvelope("event names starting with \"presence.\" or \"orbit.\" are reserved"))
		return
	}

	// Relay only the fields a client may set. The client envelope is never
	// forwarded verbatim: TTL/Metadata are subscribe-only and Sender is
	// stamped from the authenticated connection so it cannot be forged.
	b, _ := json.Marshal(core.Envelope{
		Type:    core.TypePublish,
		Channel: msg.Channel,
		Event:   msg.Event,
		Payload: msg.Payload,
		Sender:  client.UserID,
	})
	r.pubsub.Publish(ctx, msg.Channel, b)
	atomic.AddInt64(&r.metrics.MessagesPublished, 1)

	// Publishing counts as a presence heartbeat — but only on channels this
	// connection is subscribed to, so publish-only clients never appear in
	// presence (they would have no disconnect path to remove them).
	if sub, ok := r.lookupSubscription(client, msg.Channel); ok {
		r.heartbeat(ctx, client, channelSub{channel: msg.Channel, subscription: sub})
	}
}

// updatePresence is called on core.TypePing to extend TTL of all current channels.
func (r *DefaultRouter) updatePresence(ctx context.Context, client *ws.Client) {
	for _, s := range r.clientSubscriptions(client) {
		r.heartbeat(ctx, client, s)
	}
}

// heartbeat refreshes client's connection entry (and the user's presence and
// metadata expiry) for one subscription.
func (r *DefaultRouter) heartbeat(ctx context.Context, client *ws.Client, s channelSub) {
	if err := r.presence.Heartbeat(ctx, s.channel, client.UserID, client.ID, s.ttl); err != nil {
		log.Printf("presence heartbeat error on %s: %v", s.channel, err)
	}
}

// errorEnvelope builds an error frame. The message is JSON-encoded, never
// spliced into a string.
func errorEnvelope(msg string) core.Envelope {
	p, _ := json.Marshal(map[string]string{"error": msg})
	return core.Envelope{Type: "error", Payload: p}
}

// userPayload builds the {"user": ...} payload of presence events with proper
// JSON encoding (userIDs come from JWT "sub" and may contain quotes).
func userPayload(userID string) json.RawMessage {
	p, _ := json.Marshal(map[string]string{"user": userID})
	return p
}

// metadataOrEmpty returns meta if non-nil, otherwise an empty JSON object.
func metadataOrEmpty(meta json.RawMessage) json.RawMessage {
	if meta != nil {
		return meta
	}
	return json.RawMessage(`{}`)
}

// presencePayload builds the {"user": ..., "metadata": {...}} payload shared
// by presence.joined / rejoined / updated.
func presencePayload(userID string, metadata json.RawMessage) json.RawMessage {
	p, _ := json.Marshal(map[string]interface{}{
		"user":     userID,
		"metadata": metadataOrEmpty(metadata),
	})
	return p
}

// presenceEvent encodes a server-generated presence event for channel. It is
// already typed "message", so fanout delivers it unchanged.
func presenceEvent(channel, event string, payload json.RawMessage) []byte {
	b, _ := json.Marshal(core.Envelope{
		Type:    core.TypeMessage,
		Channel: channel,
		Event:   event,
		Payload: payload,
	})
	return b
}

// leaveKey returns the map key used for grace-period leave timers.
func leaveKey(channel, userID string) string {
	return channel + "\x00" + userID
}

// scheduleLeave defers Finalize (removal + presence.left) by leaveGrace, so
// the user stays visible during the grace window. If the user re-subscribes
// on this node before the timer fires, cancelLeaveTimer drops it; a resubscribe
// on another node (or after the timer fired) is caught by Finalize's atomic
// cluster-wide connection count, which then publishes nothing.
func (r *DefaultRouter) scheduleLeave(channel, userID string) {
	if r.leaveGrace <= 0 {
		r.fireLeave(channel, userID)
		return
	}
	key := leaveKey(channel, userID)
	r.leaveMu.Lock()
	if t, ok := r.leaveTimers[key]; ok {
		t.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(r.leaveGrace, func() {
		r.leaveMu.Lock()
		if r.leaveTimers[key] == t {
			delete(r.leaveTimers, key)
		}
		r.leaveMu.Unlock()
		r.fireLeave(channel, userID)
	})
	r.leaveTimers[key] = t
	r.leaveMu.Unlock()
}

// cancelLeaveTimer cancels a pending grace-period leave for userID on channel.
func (r *DefaultRouter) cancelLeaveTimer(channel, userID string) {
	key := leaveKey(channel, userID)
	r.leaveMu.Lock()
	if t, ok := r.leaveTimers[key]; ok {
		t.Stop()
		delete(r.leaveTimers, key)
	}
	r.leaveMu.Unlock()
}

// fireLeave removes userID's presence state and broadcasts presence.left —
// atomically, and only if the user has no live connection left on any node
// (so it can never announce a leave after a reconnect).
func (r *DefaultRouter) fireLeave(channel, userID string) {
	left := presenceEvent(channel, "presence.left", userPayload(userID))
	if _, err := r.presence.Finalize(context.Background(), channel, userID, left); err != nil {
		log.Printf("presence finalize error on %s: %v", channel, err)
	}
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
