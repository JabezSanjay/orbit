package router

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/orbit/orbit/internal/auth"
	"github.com/orbit/orbit/internal/core"
	"github.com/orbit/orbit/internal/pubsub"
	"github.com/orbit/orbit/internal/ws"
)

// fakeEngine is an in-memory pubsub.Engine. Publish delivers synchronously to
// the subscribed handler, standing in for the Redis round trip.
type fakeEngine struct {
	mu          sync.Mutex
	handlers    map[string]pubsub.MessageHandler
	subCalls    map[string]int
	unsubCalls  map[string]int
	published   []core.Envelope
	publishedTo []string
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		handlers:   make(map[string]pubsub.MessageHandler),
		subCalls:   make(map[string]int),
		unsubCalls: make(map[string]int),
	}
}

func (e *fakeEngine) Publish(_ context.Context, channel string, payload []byte) error {
	var env core.Envelope
	_ = json.Unmarshal(payload, &env)
	e.mu.Lock()
	e.published = append(e.published, env)
	e.publishedTo = append(e.publishedTo, channel)
	h := e.handlers[channel]
	e.mu.Unlock()
	if h != nil {
		h(channel, payload)
	}
	return nil
}

func (e *fakeEngine) Subscribe(_ context.Context, channel string, h pubsub.MessageHandler) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.handlers[channel]; ok {
		panic("double subscribe to " + channel)
	}
	e.handlers[channel] = h
	e.subCalls[channel]++
	return nil
}

func (e *fakeEngine) Unsubscribe(_ context.Context, channel string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.handlers[channel]; !ok {
		panic("unsubscribe of unsubscribed " + channel)
	}
	delete(e.handlers, channel)
	e.unsubCalls[channel]++
	return nil
}

func (e *fakeEngine) Drain(context.Context) error { return nil }
func (e *fakeEngine) Close() error                { return nil }

func (e *fakeEngine) subscribed(channel string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.handlers[channel]
	return ok
}

// fakePresence is an in-memory PresenceStore.
type fakePresence struct {
	mu      sync.Mutex
	present map[string]time.Duration // "channel\x00user" -> ttl
	removed []string
}

func newFakePresence() *fakePresence {
	return &fakePresence{present: make(map[string]time.Duration)}
}

func (p *fakePresence) Heartbeat(_ context.Context, channel, userID string, ttl time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.present[leaveKey(channel, userID)] = ttl
	return nil
}

func (p *fakePresence) Remove(_ context.Context, channel, userID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.present, leaveKey(channel, userID))
	p.removed = append(p.removed, channel+"/"+userID)
	return nil
}

func (p *fakePresence) IsPresent(_ context.Context, channel, userID string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.present[leaveKey(channel, userID)]
	return ok, nil
}

func (p *fakePresence) SetMetadata(context.Context, string, string, time.Duration, json.RawMessage) error {
	return nil
}
func (p *fakePresence) RefreshMetadata(context.Context, string, string, time.Duration) {}
func (p *fakePresence) RemoveMetadata(context.Context, string, string)                 {}

func (p *fakePresence) isPresent(channel, userID string) bool {
	ok, _ := p.IsPresent(context.Background(), channel, userID)
	return ok
}

var allChannels = &auth.ChannelPermissions{Subscribe: []string{"*"}, Publish: []string{"*"}}

func newTestClient(userID string) *ws.Client {
	// nil conn: these tests never trigger a close (slowThreshold 0).
	return ws.NewClient(userID, userID, allChannels, time.Time{}, nil, nil, nil, ws.ClientLimits{BufferSize: 1024})
}

func newTestRouter(leaveGrace time.Duration) (*DefaultRouter, *fakeEngine, *fakePresence) {
	e, p := newFakeEngine(), newFakePresence()
	return NewDefaultRouter(e, p, nil, 45*time.Second, leaveGrace), e, p
}

func subscribe(r *DefaultRouter, c *ws.Client, channel string) {
	r.HandleMessage(context.Background(), c, core.Envelope{Type: core.TypeSubscribe, Channel: channel})
}

// drain returns everything currently queued for c.
func drain(c *ws.Client) []core.Envelope {
	var out []core.Envelope
	for {
		select {
		case env := <-c.Send:
			out = append(out, env)
		default:
			return out
		}
	}
}

func (r *DefaultRouter) tableSizes() (channels, clients int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.subscriptions), len(r.clientSubs)
}

func TestEngineSubscribeFirstInLastOut(t *testing.T) {
	r, e, _ := newTestRouter(0)
	a, b := newTestClient("alice"), newTestClient("bob")

	subscribe(r, a, "room")
	subscribe(r, a, "room") // idempotent
	subscribe(r, b, "room")
	if got := e.subCalls["room"]; got != 1 {
		t.Fatalf("engine Subscribe calls = %d, want 1", got)
	}

	r.HandleDisconnect(context.Background(), a)
	if !e.subscribed("room") {
		t.Fatal("engine unsubscribed while bob is still subscribed")
	}

	r.HandleDisconnect(context.Background(), b)
	if e.subscribed("room") || e.unsubCalls["room"] != 1 {
		t.Fatalf("engine still subscribed after last client left (unsub calls %d)", e.unsubCalls["room"])
	}
	if ch, cl := r.tableSizes(); ch != 0 || cl != 0 {
		t.Fatalf("routing tables leaked: %d channels, %d clients", ch, cl)
	}
}

func TestDisconnectOnlyTouchesOwnChannels(t *testing.T) {
	r, e, p := newTestRouter(0)
	a, b := newTestClient("alice"), newTestClient("bob")
	subscribe(r, a, "a1")
	subscribe(r, a, "a2")
	subscribe(r, b, "b1")

	r.HandleDisconnect(context.Background(), a)

	if e.subscribed("a1") || e.subscribed("a2") {
		t.Fatal("alice's channels should be unsubscribed")
	}
	if !e.subscribed("b1") {
		t.Fatal("bob's channel must not be touched")
	}
	// leaveGrace 0 fires presence removal in a goroutine.
	waitFor(t, func() bool { return !p.isPresent("a1", "alice") && !p.isPresent("a2", "alice") })
	if !p.isPresent("b1", "bob") {
		t.Fatal("bob's presence must not be touched")
	}
}

func TestSubscriberReceivesOwnJoin(t *testing.T) {
	r, _, _ := newTestRouter(0)
	a := newTestClient("alice")
	subscribe(r, a, "room")

	msgs := drain(a)
	if len(msgs) != 1 || msgs[0].Event != "presence.joined" || msgs[0].Type != core.TypeMessage {
		t.Fatalf("got %+v, want one presence.joined message", msgs)
	}
}

func TestPublishHeartbeatsOnlySubscribedChannels(t *testing.T) {
	r, _, p := newTestRouter(0)
	a := newTestClient("alice")
	r.HandleMessage(context.Background(), a, core.Envelope{Type: core.TypePublish, Channel: "elsewhere", Payload: json.RawMessage(`{}`)})
	if p.isPresent("elsewhere", "alice") {
		t.Fatal("publish-only client must not appear in presence")
	}
}

func TestPingRefreshesAllSubscriptionsWithTheirTTL(t *testing.T) {
	r, _, p := newTestRouter(0)
	a := newTestClient("alice")
	subscribe(r, a, "x")
	r.HandleMessage(context.Background(), a, core.Envelope{Type: core.TypeSubscribe, Channel: "y", TTL: 10})

	p.mu.Lock()
	p.present = map[string]time.Duration{}
	p.mu.Unlock()

	r.HandleMessage(context.Background(), a, core.Envelope{Type: core.TypePing})
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.present[leaveKey("x", "alice")] != 45*time.Second || p.present[leaveKey("y", "alice")] != 10*time.Second {
		t.Fatalf("unexpected heartbeats after ping: %v", p.present)
	}
}

// TestConcurrentSubscribeDisconnectKeepsEngineConsistent races subscribes and
// disconnects on a small set of channels while fanout runs, then checks the
// invariant: the engine is subscribed to a channel iff it has local clients.
// The fake engine panics on double subscribe / spurious unsubscribe.
func TestConcurrentSubscribeDisconnectKeepsEngineConsistent(t *testing.T) {
	r, e, _ := newTestRouter(time.Millisecond)
	channels := []string{"c0", "c1", "c2"}

	stop := make(chan struct{})
	var fanout sync.WaitGroup
	fanout.Add(1)
	go func() { // concurrent fanout readers
		defer fanout.Done()
		for {
			select {
			case <-stop:
				return
			default:
				for _, ch := range channels {
					r.handleRedisMessage(ch, []byte(`{"type":"publish","channel":"`+ch+`"}`))
				}
			}
		}
	}()

	var wg sync.WaitGroup
	survivors := make([]*ws.Client, 16)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 200; i++ {
				c := newTestClient(fmt.Sprintf("u%d", w))
				subscribe(r, c, channels[rng.Intn(len(channels))])
				subscribe(r, c, channels[rng.Intn(len(channels))])
				drain(c)
				if i == 199 && w%2 == 0 {
					survivors[w] = c // leave some clients connected
					continue
				}
				r.HandleDisconnect(context.Background(), c)
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	fanout.Wait()

	for _, ch := range channels {
		r.mu.RLock()
		local := len(r.subscriptions[ch]) > 0
		r.mu.RUnlock()
		if local != e.subscribed(ch) {
			t.Fatalf("channel %s: local clients=%v but engine subscribed=%v", ch, local, e.subscribed(ch))
		}
	}

	for _, c := range survivors {
		if c != nil {
			r.HandleDisconnect(context.Background(), c)
		}
	}
	if ch, cl := r.tableSizes(); ch != 0 || cl != 0 {
		t.Fatalf("routing tables leaked: %d channels, %d clients", ch, cl)
	}
	for _, ch := range channels {
		if e.subscribed(ch) {
			t.Fatalf("engine still subscribed to %s with no local clients", ch)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestClientCannotPublishReservedEvents(t *testing.T) {
	for _, event := range []string{"presence.joined", "presence.left", "orbit.anything"} {
		r, e, _ := newTestRouter(0)
		a := newTestClient("alice")
		subscribe(r, a, "room")
		drain(a)
		before := len(e.published)

		r.HandleMessage(context.Background(), a, core.Envelope{
			Type: core.TypePublish, Channel: "room", Event: event,
			Payload: json.RawMessage(`{"user":"bob"}`),
		})

		if len(e.published) != before {
			t.Fatalf("%s: reserved event was relayed", event)
		}
		msgs := drain(a)
		if len(msgs) != 1 || msgs[0].Type != "error" {
			t.Fatalf("%s: got %+v, want one error envelope", event, msgs)
		}
	}
}

func TestPublishStampsSenderAndStripsSubscribeFields(t *testing.T) {
	r, _, _ := newTestRouter(0)
	a, b := newTestClient("alice"), newTestClient("bob")
	subscribe(r, a, "room")
	subscribe(r, b, "room")
	drain(a)
	drain(b)

	r.HandleMessage(context.Background(), a, core.Envelope{
		Type: core.TypePublish, Channel: "room", Event: "cursor",
		Payload:  json.RawMessage(`{"x":1}`),
		Sender:   "mallory", // forged; must be overwritten
		TTL:      3600,
		Metadata: json.RawMessage(`{"evil":true}`),
	})

	msgs := drain(b)
	if len(msgs) != 1 {
		t.Fatalf("bob got %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Type != core.TypeMessage || m.Event != "cursor" || m.Sender != "alice" || m.TTL != 0 || m.Metadata != nil || string(m.Payload) != `{"x":1}` {
		t.Fatalf("unexpected relayed envelope: %+v", m)
	}
}

func TestPresencePayloadsAreJSONEncoded(t *testing.T) {
	r, e, _ := newTestRouter(0)
	evil := `x","admin":true,"y":"`
	a := newTestClient(evil)
	subscribe(r, a, "room")
	r.fireLeave("room", evil)

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, env := range e.published {
		var p map[string]interface{}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("%s payload is not valid JSON: %s", env.Event, env.Payload)
		}
		if p["user"] != evil || p["admin"] != nil {
			t.Fatalf("%s payload was injectable: %s", env.Event, env.Payload)
		}
	}
}
