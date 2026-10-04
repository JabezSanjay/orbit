package ws

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// run feeds frames to l at hz for d starting at t0 and returns drops,
// notifications, and the time abuse was first flagged (zero if never).
func run(l *inboundLimiter, t0 time.Time, hz int, d time.Duration) (drops, notes int, abusedAt time.Duration) {
	step := time.Second / time.Duration(hz)
	for at := time.Duration(0); at < d; at += step {
		now := t0.Add(at)
		if l.allow(now) {
			continue
		}
		drops++
		notify, abusive := l.dropped(now)
		if notify {
			notes++
		}
		if abusive && abusedAt == 0 {
			abusedAt = at
		}
	}
	return
}

func TestInboundLimiterDisabled(t *testing.T) {
	if newInboundLimiter(0, time.Now()) != nil {
		t.Fatal("perSec 0 must disable limiting")
	}
}

func TestInboundLimiterAllowsCursorTraffic(t *testing.T) {
	t0 := time.Now()
	l := newInboundLimiter(100, t0)
	// 60 Hz cursor moves plus a ping every 25s, for a minute.
	drops, _, _ := run(l, t0, 60, time.Minute)
	if drops != 0 {
		t.Fatalf("60 Hz traffic was rate limited (%d drops)", drops)
	}
}

func TestInboundLimiterFloodIsDroppedThenClosed(t *testing.T) {
	t0 := time.Now()
	l := newInboundLimiter(100, t0)
	drops, notes, abusedAt := run(l, t0, 1000, 10*time.Second)
	if drops == 0 {
		t.Fatal("1000 Hz flood was not limited")
	}
	if abusedAt == 0 || abusedAt < 3*time.Second || abusedAt > 4*time.Second {
		t.Fatalf("abuse flagged at %v, want after ~3 consecutive seconds", abusedAt)
	}
	// One error reply per second at most.
	if notes > 10 {
		t.Fatalf("%d error notifications in 10s, want <= 10", notes)
	}
}

func TestInboundLimiterShortBurstNotAbusive(t *testing.T) {
	t0 := time.Now()
	l := newInboundLimiter(100, t0)
	// A 1.5s burst at 400 Hz, then normal traffic.
	_, _, abusedAt := run(l, t0, 400, 1500*time.Millisecond)
	if abusedAt != 0 {
		t.Fatalf("short burst flagged as abuse at %v", abusedAt)
	}
	drops, _, abusedAt := run(l, t0.Add(1500*time.Millisecond), 50, 10*time.Second)
	if abusedAt != 0 || drops != 0 {
		t.Fatalf("normal traffic after burst: drops=%d abusedAt=%v", drops, abusedAt)
	}
}

// TestOversizedFrameClosesWith1009 checks ORBIT_MAX_MESSAGE_BYTES is enforced.
func TestOversizedFrameClosesWith1009(t *testing.T) {
	serverConn, peer := newConnPair(t)
	g := NewGateway(0)
	go g.Run()
	defer g.once.Do(func() { close(g.done) })

	c := NewClient("id", "user", nil, time.Time{}, serverConn, g, nopRouter{}, ClientLimits{BufferSize: 8, MaxMessageBytes: 1024})
	g.Register <- c
	go c.WritePump()
	go c.ReadPump()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	big := `{"type":"publish","channel":"x","payload":"` + strings.Repeat("a", 2048) + `"}`
	if err := peer.Write(ctx, websocket.MessageText, []byte(big)); err != nil {
		t.Fatal(err)
	}
	_, _, err := peer.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusMessageTooBig {
		t.Fatalf("peer close status = %v (err %v), want 1009", got, err)
	}
}
