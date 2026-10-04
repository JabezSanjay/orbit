package ws

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/orbit/orbit/internal/auth"
	"github.com/orbit/orbit/internal/core"
	"github.com/orbit/orbit/internal/metrics"
)

const (
	writeWait  = 5 * time.Second
	pingPeriod = 15 * time.Second

	// StatusTokenExpired is the application close code sent when the
	// connection's auth token expires while it is still connected.
	StatusTokenExpired websocket.StatusCode = 4001
)

type Router interface {
	HandleMessage(ctx context.Context, client *Client, msg core.Envelope)
	HandleDisconnect(ctx context.Context, client *Client)
}

// Client represents a single connected WebSocket user.
type Client struct {
	ID          string
	UserID      string
	Permissions *auth.ChannelPermissions
	// ExpiresAt is when the connection's auth token expires; zero means never.
	ExpiresAt time.Time
	Conn      *websocket.Conn
	Send      chan core.Envelope

	Gateway *Gateway
	Router  Router

	// Slow-consumer detection: counts consecutive dropped messages.
	// Reset to 0 on any successful queue. When it reaches slowThreshold the
	// client is disconnected with 1008 Policy Violation.
	slowThreshold int
	dropCount     atomic.Int32

	// Inbound limits: frame size cap and per-connection rate limiter
	// (nil when disabled). The limiter is only touched by ReadPump.
	maxMessageBytes int64
	inbound         *inboundLimiter

	ctx        context.Context
	cancelFunc context.CancelFunc

	// closeOnce guards Close so the first close code/reason wins and the
	// close handshake runs exactly once. closing is set synchronously so
	// SendJSON stops queueing as soon as a close has been requested.
	closeOnce sync.Once
	closing   atomic.Bool
}

func NewClient(id, userID string, perms *auth.ChannelPermissions, expiresAt time.Time, conn *websocket.Conn, gateway *Gateway, router Router, limits ClientLimits) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	maxBytes := limits.MaxMessageBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessageBytes
	}
	return &Client{
		ID:              id,
		UserID:          userID,
		Permissions:     perms,
		ExpiresAt:       expiresAt,
		Conn:            conn,
		Send:            make(chan core.Envelope, limits.BufferSize),
		Gateway:         gateway,
		Router:          router,
		slowThreshold:   limits.SlowThreshold,
		maxMessageBytes: maxBytes,
		inbound:         newInboundLimiter(limits.MessagesPerSec, time.Now()),
		ctx:             ctx,
		cancelFunc:      cancel,
	}
}

// teardown detaches a disconnected client from the router and gateway.
func (c *Client) teardown() {
	// Teardown order matters for safety:
	//  1. Remove the client from the router first. HandleDisconnect takes the
	//     router's write lock, which waits out any fanout worker currently
	//     iterating this client's channels, so no new fanout can reach it.
	//  2. Cancel ctx so WritePump exits and SendJSON short-circuits.
	//  3. Unregister from the gateway (bookkeeping only).
	// c.Send is never closed: a late SendJSON from any goroutine is a
	// harmless buffered write or drop, never a send-on-closed-channel panic.
	c.Router.HandleDisconnect(context.Background(), c)
	c.cancelFunc()
	c.Gateway.unregister(c)
}

// ReadPump handles messages arriving from the WebSocket connection.
func (c *Client) ReadPump() {
	defer c.teardown()

	// Frames larger than this make the library close the connection with
	// 1009 Message Too Big.
	c.Conn.SetReadLimit(c.maxMessageBytes)

	for {
		var env core.Envelope
		err := wsjson.Read(c.ctx, c.Conn, &env)
		if err != nil {
			if websocket.CloseStatus(err) != -1 {
				log.Printf("Client %s closed connection normal: %v", c.UserID, err)
			} else {
				log.Printf("Client %s read error: %v", c.UserID, err)
			}
			break
		}

		// A close has been requested (slow consumer, token expiry, abuse,
		// shutdown): keep reading until the handshake completes, but stop
		// routing frames.
		if c.closing.Load() {
			continue
		}

		if c.inbound != nil {
			now := time.Now()
			if !c.inbound.allow(now) {
				metrics.InboundRateLimitedTotal.Inc()
				notify, abusive := c.inbound.dropped(now)
				if abusive {
					log.Printf("Rate limit: disconnecting client %s (sustained inbound flood)", c.UserID)
					c.Close(websocket.StatusPolicyViolation, "rate limit exceeded")
					continue
				}
				if notify {
					c.SendJSON(rateLimitedEnvelope)
				}
				continue
			}
		}

		// Immediate response for ping frames over standard JSON protocol
		if env.Type == core.TypePing {
			c.SendJSON(core.Envelope{Type: core.TypePong})
		}

		c.Router.HandleMessage(c.ctx, c, env)
	}
}

// rateLimitedEnvelope is the error reply for a dropped inbound frame. It is
// sent at most once per second per connection.
var rateLimitedEnvelope = core.Envelope{Type: "error", Payload: []byte(`{"error":"rate limit exceeded"}`)}

// WritePump pushes messages to the client. Also handles periodic protocol-level
// pings and closes the connection with 4001 when the auth token expires.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)

	// Token expiry: auth only validates exp at connect time, so enforce it for
	// the lifetime of the connection. A nil channel never fires.
	var expired <-chan time.Time
	if !c.ExpiresAt.IsZero() {
		t := time.NewTimer(time.Until(c.ExpiresAt))
		defer t.Stop()
		expired = t.C
	}

	defer func() {
		ticker.Stop()
		// No-op if a close was already requested (e.g. slow consumer, shutdown)
		// or the peer already went away.
		c.Close(websocket.StatusInternalError, "write pump closed")
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		case message := <-c.Send:
			ctx, cancel := context.WithTimeout(c.ctx, writeWait)
			err := wsjson.Write(ctx, c.Conn, message)
			cancel()
			if err != nil {
				log.Printf("Failed writing to client %s: %v", c.UserID, err)
				return
			}

		case <-expired:
			log.Printf("Token expired: disconnecting client %s", c.UserID)
			c.Close(StatusTokenExpired, "token expired")
			return

		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.ctx, writeWait)
			err := c.Conn.Ping(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// Close closes the connection with the given status code and reason without
// blocking the caller. It is safe to call from any goroutine (fanout workers
// in particular, which must never block on a slow peer) and any number of
// times; only the first call has an effect.
//
// The close handshake (up to ~10s in coder/websocket) runs in its own
// goroutine. ctx is cancelled only after the close frame has been written:
// cancelling a read ctx first makes the library drop the TCP connection
// without a close frame, so the peer would see 1006 instead of code.
func (c *Client) Close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.closing.Store(true)
		go func() {
			defer c.cancelFunc()
			_ = c.Conn.Close(code, reason)
		}()
	})
}

// SendJSON queues env for delivery. It never blocks: when the outbound buffer
// is full the message is dropped, and after slowThreshold consecutive drops
// the client is closed asynchronously with 1008.
func (c *Client) SendJSON(env core.Envelope) {
	if c.closing.Load() {
		return
	}
	select {
	case c.Send <- env:
		// Successful queue — reset the consecutive drop counter.
		c.dropCount.Store(0)
	case <-c.ctx.Done():
		return
	default:
		// Buffer full: client is reading too slowly.
		metrics.DroppedMessagesTotal.Inc()
		drops := c.dropCount.Add(1)
		if c.slowThreshold > 0 && int(drops) >= c.slowThreshold {
			log.Printf("Slow consumer: disconnecting client %s (%d consecutive drops)", c.UserID, drops)
			c.Close(websocket.StatusPolicyViolation, "slow consumer")
		}
	}
}
