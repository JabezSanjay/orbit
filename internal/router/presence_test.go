package router

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/orbit/orbit/internal/auth"
	"github.com/orbit/orbit/internal/core"
	"github.com/orbit/orbit/internal/ws"
)

// Presence refcount semantics (issue draft 25): events fire only on a user's
// cluster-wide 0 -> 1 and 1 -> 0 transitions.

func TestTwoConnectionsSameUserJoinOnceLeaveOnce(t *testing.T) {
	const grace = 20 * time.Millisecond
	r, _, p := newTestRouter(grace)
	watcher := newTestClient("bob")
	subscribe(r, watcher, "room")
	drain(watcher)

	tab1, tab2 := newTestClient("alice"), newTestClient("alice")
	subscribe(r, tab1, "room")
	subscribe(r, tab2, "room")
	if got := drain(watcher); countEvent(got, "presence.joined") != 1 || len(got) != 1 {
		t.Fatalf("watcher got %v, want exactly one presence.joined", events(got))
	}

	r.HandleDisconnect(context.Background(), tab1)
	time.Sleep(3 * grace)
	if got := drain(watcher); len(got) != 0 {
		t.Fatalf("closing one of two tabs emitted %v", events(got))
	}
	if !p.isPresent("room", "alice") {
		t.Fatal("alice must stay present while tab2 is connected")
	}

	r.HandleDisconnect(context.Background(), tab2)
	if got := drain(watcher); len(got) != 0 {
		t.Fatalf("presence.left must wait for the grace period, got %v", events(got))
	}
	waitFor(t, func() bool { return !p.isPresent("room", "alice") })
	time.Sleep(3 * grace)
	if got := drain(watcher); countEvent(got, "presence.left") != 1 || len(got) != 1 {
		t.Fatalf("watcher got %v, want exactly one presence.left", events(got))
	}
}

func TestCrossNodeRefcount(t *testing.T) {
	const grace = 20 * time.Millisecond
	nodeA, nodeB, p := newTestCluster(grace)
	watcherA, watcherB := newTestClient("bob"), newTestClient("carol")
	subscribe(nodeA, watcherA, "room")
	subscribe(nodeB, watcherB, "room")
	drain(watcherA)
	drain(watcherB)

	onA, onB := newTestClient("alice"), newTestClient("alice")
	subscribe(nodeA, onA, "room")
	subscribe(nodeB, onB, "room")
	for _, w := range []*ws.Client{watcherA, watcherB} {
		if got := drain(w); countEvent(got, "presence.joined") != 1 || len(got) != 1 {
			t.Fatalf("watcher %s got %v, want one presence.joined", w.UserID, events(got))
		}
	}

	nodeA.HandleDisconnect(context.Background(), onA)
	time.Sleep(3 * grace)
	if got := drain(watcherB); len(got) != 0 {
		t.Fatalf("closing alice's node-A connection emitted %v", events(got))
	}
	if !p.isPresent("room", "alice") {
		t.Fatal("alice must stay present while connected to node B")
	}

	nodeB.HandleDisconnect(context.Background(), onB)
	waitFor(t, func() bool { return !p.isPresent("room", "alice") })
	time.Sleep(3 * grace)
	if got := drain(watcherA); countEvent(got, "presence.left") != 1 || len(got) != 1 {
		t.Fatalf("watcher A got %v, want exactly one presence.left", events(got))
	}
}

// TestFiredLeaveTimerAfterResubscribeOnOtherNode covers the race where a
// grace timer fires after the user already reconnected on another node: the
// node that owns the timer cannot cancel it, so Finalize must see the new
// connection and publish nothing.
func TestFiredLeaveTimerAfterResubscribeOnOtherNode(t *testing.T) {
	nodeA, nodeB, p := newTestCluster(time.Hour) // timer never fires on its own
	watcher := newTestClient("bob")
	subscribe(nodeB, watcher, "room")

	old := newTestClient("alice")
	subscribe(nodeA, old, "room")
	nodeA.HandleDisconnect(context.Background(), old) // grace timer pending on A
	drain(watcher)

	fresh := newTestClient("alice")
	subscribe(nodeB, fresh, "room") // B cannot cancel A's timer
	if got := drain(watcher); len(got) != 1 || got[0].Event != "presence.rejoined" {
		t.Fatalf("watcher got %v, want presence.rejoined", events(got))
	}

	nodeA.fireLeave("room", "alice") // the timer firing late
	if got := drain(watcher); len(got) != 0 {
		t.Fatalf("stale leave timer emitted %v after reconnect", events(got))
	}
	if !p.isPresent("room", "alice") {
		t.Fatal("stale leave timer removed a connected user")
	}
}

// TestRejoinWithinGraceOnSameNode preserves the PR #33 behaviour: a reconnect
// inside the grace window cancels the leave and announces presence.rejoined.
func TestRejoinWithinGraceOnSameNode(t *testing.T) {
	const grace = 30 * time.Millisecond
	r, _, _ := newTestRouter(grace)
	watcher := newTestClient("bob")
	subscribe(r, watcher, "room")

	old := newTestClient("alice")
	subscribe(r, old, "room")
	r.HandleDisconnect(context.Background(), old)
	drain(watcher)

	subscribe(r, newTestClient("alice"), "room")
	time.Sleep(3 * grace)
	if got := drain(watcher); len(got) != 1 || got[0].Event != "presence.rejoined" {
		t.Fatalf("watcher got %v, want only presence.rejoined", events(got))
	}
}

func TestUnsubscribeWithAnotherConnectionKeepsUserPresent(t *testing.T) {
	r, _, p := newTestRouter(0)
	watcher := newTestClient("bob")
	subscribe(r, watcher, "room")
	tab1, tab2 := newTestClient("alice"), newTestClient("alice")
	subscribe(r, tab1, "room")
	subscribe(r, tab2, "room")
	drain(watcher)

	unsubscribe(r, tab1, "room")
	if got := drain(watcher); len(got) != 0 {
		t.Fatalf("unsubscribe of one of two connections emitted %v", events(got))
	}
	if !p.isPresent("room", "alice") {
		t.Fatal("alice must stay present")
	}

	unsubscribe(r, tab2, "room")
	if got := drain(watcher); len(got) != 1 || got[0].Event != "presence.left" {
		t.Fatalf("watcher got %v, want one presence.left", events(got))
	}
}

func TestSecondConnectionMetadataBroadcastsUpdated(t *testing.T) {
	r, _, p := newTestRouter(0)
	watcher := newTestClient("bob")
	subscribe(r, watcher, "room")
	subscribe(r, newTestClient("alice"), "room")
	drain(watcher)

	r.HandleMessage(context.Background(), newTestClient("alice"), core.Envelope{
		Type: core.TypeSubscribe, Channel: "room", Metadata: json.RawMessage(`{"color":"red"}`),
	})
	got := drain(watcher)
	if len(got) != 1 || got[0].Event != "presence.updated" {
		t.Fatalf("watcher got %v, want one presence.updated", events(got))
	}
	var pl struct {
		User     string          `json:"user"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(got[0].Payload, &pl); err != nil || pl.User != "alice" || string(pl.Metadata) != `{"color":"red"}` {
		t.Fatalf("unexpected presence.updated payload %s", got[0].Payload)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if string(p.meta[leaveKey("room", "alice")]) != `{"color":"red"}` {
		t.Fatal("metadata was not stored (last write wins)")
	}
}

// presenceState extracts the users of the single presence.state in msgs.
func presenceState(t *testing.T, msgs []core.Envelope) []map[string]json.RawMessage {
	t.Helper()
	var users []map[string]json.RawMessage
	n := 0
	for _, m := range msgs {
		if m.Event != "presence.state" {
			continue
		}
		n++
		var pl struct {
			Users []map[string]json.RawMessage `json:"users"`
		}
		if err := json.Unmarshal(m.Payload, &pl); err != nil || pl.Users == nil {
			t.Fatalf("bad presence.state payload %s", m.Payload)
		}
		users = pl.Users
	}
	if n != 1 {
		t.Fatalf("got %d presence.state frames in %v, want 1", n, events(msgs))
	}
	return users
}

func TestSubscribeSendsPresenceSnapshotToSubscriberOnly(t *testing.T) {
	r, _, _ := newTestRouter(0)
	alice := newTestClient("alice")
	r.HandleMessage(context.Background(), alice, core.Envelope{
		Type: core.TypeSubscribe, Channel: "room", Metadata: json.RawMessage(`{"name":"Alice"}`),
	})
	if users := presenceState(t, drain(alice)); len(users) != 1 || string(users[0]["user"]) != `"alice"` {
		t.Fatalf("snapshot of a fresh channel = %v, want only the subscriber", users)
	}

	bob := newTestClient("bob")
	subscribe(r, bob, "room")
	users := presenceState(t, drain(bob))
	if len(users) != 2 || string(users[0]["user"]) != `"alice"` || string(users[0]["metadata"]) != `{"name":"Alice"}` ||
		string(users[1]["user"]) != `"bob"` || string(users[1]["metadata"]) != `{}` {
		t.Fatalf("unexpected snapshot %v", users)
	}
	if got := drain(alice); countEvent(got, "presence.state") != 0 || countEvent(got, "presence.joined") != 1 {
		t.Fatalf("existing subscriber got %v, want only bob's presence.joined", events(got))
	}

	subscribe(r, bob, "room") // idempotent: no second snapshot
	if got := drain(bob); len(got) != 0 {
		t.Fatalf("duplicate subscribe sent %v", events(got))
	}
}

func TestRejectedSubscribeSendsNoSnapshot(t *testing.T) {
	r, _, _ := newTestRouter(0)
	limited := ws.NewClient("x", "mallory", &auth.ChannelPermissions{Subscribe: []string{"public-*"}}, time.Time{}, nil, nil, nil, ws.ClientLimits{BufferSize: 16})
	subscribe(r, limited, "private")
	r.HandleMessage(context.Background(), limited, core.Envelope{Type: core.TypeSubscribe, Channel: "public-1", TTL: 1})
	got := drain(limited)
	if len(got) != 2 || got[0].Type != "error" || got[1].Type != "error" {
		t.Fatalf("got %+v, want two error frames and no snapshot", got)
	}
}
