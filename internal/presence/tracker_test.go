package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// These tests exercise the Lua scripts against a real Redis. They are skipped
// unless ORBIT_TEST_REDIS_URL is set, e.g.
//
//	ORBIT_TEST_REDIS_URL=redis://localhost:6379/15 go test ./internal/presence/
//
// They only touch keys under a per-run channel name.
func testTracker(t *testing.T) (*Tracker, *redis.Client, string) {
	t.Helper()
	url := os.Getenv("ORBIT_TEST_REDIS_URL")
	if url == "" {
		t.Skip("ORBIT_TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { c.Close() })
	return NewTracker(c), c, fmt.Sprintf("tracker-test-%s-%d", t.Name(), time.Now().UnixNano())
}

var testEvents = JoinEvents{Joined: []byte("joined"), Rejoined: []byte("rejoined"), Updated: []byte("updated")}

func TestTrackerRefcountTransitions(t *testing.T) {
	tr, c, ch := testTracker(t)
	ctx := context.Background()
	sub := c.Subscribe(ctx, ch)
	defer sub.Close()
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	msgs := sub.Channel()
	expect := func(want string) {
		t.Helper()
		select {
		case m := <-msgs:
			if m.Payload != want {
				t.Fatalf("published %q, want %q", m.Payload, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("nothing published, want %q", want)
		}
	}
	expectNone := func() {
		t.Helper()
		select {
		case m := <-msgs:
			t.Fatalf("unexpected publish %q", m.Payload)
		case <-time.After(100 * time.Millisecond):
		}
	}

	if res, err := tr.Join(ctx, ch, "alice", "c1", time.Minute, json.RawMessage(`{"a":1}`), testEvents); err != nil || res != JoinJoined {
		t.Fatalf("first join = %v, %v; want JoinJoined", res, err)
	}
	expect("joined")
	if res, _ := tr.Join(ctx, ch, "alice", "c2", time.Minute, nil, testEvents); res != JoinNone {
		t.Fatalf("second connection join = %v, want JoinNone", res)
	}
	expectNone()
	if res, _ := tr.Join(ctx, ch, "alice", "c3", time.Minute, json.RawMessage(`{"a":2}`), testEvents); res != JoinUpdated {
		t.Fatalf("third connection with metadata = %v, want JoinUpdated", res)
	}
	expect("updated")

	if n, _ := tr.Count(ctx, ch); n != 1 {
		t.Fatalf("Count = %d, want 1 (users, not connections)", n)
	}
	entries, _ := tr.GetUsersWithMetadata(ctx, ch)
	if len(entries) != 1 || string(entries[0].Metadata) != `{"a":2}` {
		t.Fatalf("GetUsersWithMetadata = %+v", entries)
	}

	for i, conn := range []string{"c1", "c2"} {
		if n, _ := tr.Leave(ctx, ch, "alice", conn); n != int64(2-i) {
			t.Fatalf("Leave(%s) remaining = %d, want %d", conn, n, 2-i)
		}
	}
	if ok, _ := tr.Finalize(ctx, ch, "alice", []byte("left")); ok {
		t.Fatal("Finalize published while c3 is live")
	}
	expectNone()

	if n, _ := tr.Leave(ctx, ch, "alice", "c3"); n != 0 {
		t.Fatalf("last Leave remaining = %d, want 0", n)
	}
	// Grace window: still listed; a reconnect is a rejoin.
	if n, _ := tr.Count(ctx, ch); n != 1 {
		t.Fatalf("user should stay listed until Finalize, Count = %d", n)
	}
	if res, _ := tr.Join(ctx, ch, "alice", "c4", time.Minute, nil, testEvents); res != JoinRejoined {
		t.Fatalf("join within grace = %v, want JoinRejoined", res)
	}
	expect("rejoined")
	if ok, _ := tr.Finalize(ctx, ch, "alice", []byte("left")); ok {
		t.Fatal("stale Finalize published after rejoin")
	}

	tr.Leave(ctx, ch, "alice", "c4")
	if ok, _ := tr.Finalize(ctx, ch, "alice", []byte("left")); !ok {
		t.Fatal("Finalize with no connections did not publish")
	}
	expect("left")
	if ok, _ := tr.Finalize(ctx, ch, "alice", []byte("left")); ok {
		t.Fatal("second Finalize published again")
	}
	if n, _ := tr.Count(ctx, ch); n != 0 {
		t.Fatalf("Count after leave = %d", n)
	}
	if n, _ := c.Exists(ctx, metaKey(ch, "alice"), connsKey(ch, "alice")).Result(); n != 0 {
		t.Fatalf("%d presence keys left behind", n)
	}
}

func TestTrackerUpdateMetadata(t *testing.T) {
	tr, c, ch := testTracker(t)
	ctx := context.Background()
	tr.Join(ctx, ch, "alice", "c1", time.Minute, nil, testEvents)
	if err := tr.UpdateMetadata(ctx, ch, "alice", "c1", time.Minute, json.RawMessage(`{"s":"away"}`), []byte("updated")); err != nil {
		t.Fatal(err)
	}
	entries, _ := tr.GetUsersWithMetadata(ctx, ch)
	if len(entries) != 1 || string(entries[0].Metadata) != `{"s":"away"}` {
		t.Fatalf("after update: %+v", entries)
	}
	tr.UpdateMetadata(ctx, ch, "alice", "c1", time.Minute, nil, []byte("updated"))
	if n, _ := c.Exists(ctx, metaKey(ch, "alice")).Result(); n != 0 {
		t.Fatal("nil metadata did not clear the key")
	}
	tr.Leave(ctx, ch, "alice", "c1")
	tr.Finalize(ctx, ch, "alice", []byte("left"))
}

func TestTrackerExpiredConnectionStopsCounting(t *testing.T) {
	tr, _, ch := testTracker(t)
	ctx := context.Background()
	tr.Join(ctx, ch, "alice", "dead", time.Second, nil, testEvents)
	tr.Join(ctx, ch, "alice", "live", time.Minute, nil, testEvents)
	time.Sleep(1100 * time.Millisecond)
	tr.Heartbeat(ctx, ch, "alice", "live", time.Minute)
	if n, _ := tr.Leave(ctx, ch, "alice", "live"); n != 0 {
		t.Fatalf("expired connection still counted: remaining = %d", n)
	}
}
