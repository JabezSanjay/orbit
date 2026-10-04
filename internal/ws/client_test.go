package ws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JabezSanjay/orbit/internal/core"
	"github.com/coder/websocket"
)

// newConnPair returns a server-side *websocket.Conn and the dialled peer.
func newConnPair(t *testing.T) (server, peer *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		accepted <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { peer.CloseNow() })
	select {
	case server = <-accepted:
	case <-ctx.Done():
		t.Fatal("server never accepted")
	}
	t.Cleanup(func() { server.CloseNow() })
	return server, peer
}

// nopRouter satisfies Router without doing anything.
type nopRouter struct{}

func (nopRouter) HandleMessage(context.Context, *Client, core.Envelope) {}
func (nopRouter) HandleDisconnect(context.Context, *Client)             {}

// TestSendJSONAfterTeardownNeverPanics hammers SendJSON from several goroutines
// (standing in for fanout workers) while the client is torn down and
// unregistered. Before the fix, Gateway.Run closed client.Send and a concurrent
// SendJSON could panic with "send on closed channel".
func TestSendJSONAfterTeardownNeverPanics(t *testing.T) {
	for i := 0; i < 200; i++ {
		g := NewGateway(0)
		go g.Run()

		// slowThreshold 0: drops never trigger a close (there is no real conn).
		c := NewClient("id", "user", nil, time.Time{}, nil, g, nopRouter{}, ClientLimits{BufferSize: 4})
		g.Register <- c

		var wg sync.WaitGroup
		start := make(chan struct{})
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for n := 0; n < 50; n++ {
					c.SendJSON(core.Envelope{Type: core.TypeMessage})
				}
			}()
		}
		close(start)
		c.teardown()
		wg.Wait()

		// Sends after full teardown must also be safe.
		c.SendJSON(core.Envelope{Type: core.TypeMessage})

		g.once.Do(func() { close(g.done) }) // stop Run without touching the nil conn
	}
}

// TestSlowConsumerCloseDoesNotBlock verifies the slow-consumer path returns
// immediately (it runs on fanout workers under the router read lock) even
// though the peer is not reading, and that the peer still receives 1008.
func TestSlowConsumerCloseDoesNotBlock(t *testing.T) {
	serverConn, peer := newConnPair(t)

	// Buffer of 1 and no WritePump: the second send drops, the third trips
	// the threshold of 2 consecutive drops.
	c := NewClient("id", "user", nil, time.Time{}, serverConn, nil, nopRouter{}, ClientLimits{BufferSize: 1, SlowThreshold: 2})

	start := time.Now()
	for i := 0; i < 10; i++ {
		c.SendJSON(core.Envelope{Type: core.TypeMessage})
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("SendJSON blocked for %v on the slow-consumer path", d)
	}
	if !c.closing.Load() {
		t.Fatal("expected client to be closing after exceeding the slow threshold")
	}

	// Nothing was written (no WritePump), so the first frame is the close.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := peer.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Fatalf("peer close status = %v (err %v), want 1008", got, err)
	}

	select {
	case <-c.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client ctx was not cancelled after close")
	}
}

// TestTokenExpiryClosesWith4001 verifies a live connection is closed with
// 4001 "token expired" once its token's exp passes.
func TestTokenExpiryClosesWith4001(t *testing.T) {
	serverConn, peer := newConnPair(t)
	c := NewClient("id", "user", nil, time.Now().Add(100*time.Millisecond), serverConn, nil, nopRouter{}, ClientLimits{BufferSize: 8})
	go c.WritePump()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := peer.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != StatusTokenExpired || ce.Reason != "token expired" {
		t.Fatalf("peer got %v, want close 4001 \"token expired\"", err)
	}
}
