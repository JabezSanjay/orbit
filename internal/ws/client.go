package ws

import (
	"context"
	"log"
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
	Conn        *websocket.Conn
	Send        chan core.Envelope

	Gateway *Gateway
	Router  Router

	// Slow-consumer detection: counts consecutive dropped messages.
	// Reset to 0 on any successful queue. When it reaches slowThreshold the
	// client is disconnected with 1008 Policy Violation.
	slowThreshold int
	dropCount     atomic.Int32

	ctx        context.Context
	cancelFunc context.CancelFunc
}

func NewClient(id, userID string, perms *auth.ChannelPermissions, conn *websocket.Conn, gateway *Gateway, router Router, bufSize, slowThreshold int) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		ID:            id,
		UserID:        userID,
		Permissions:   perms,
		Conn:          conn,
		Send:          make(chan core.Envelope, bufSize),
		Gateway:       gateway,
		Router:        router,
		slowThreshold: slowThreshold,
		ctx:           ctx,
		cancelFunc:    cancel,
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

	c.Conn.SetReadLimit(1024 * 1024)

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

		// Immediate response for ping frames over standard JSON protocol
		if env.Type == core.TypePing {
			c.SendJSON(core.Envelope{Type: core.TypePong})
		}

		c.Router.HandleMessage(c.ctx, c, env)
	}
}

// WritePump pushes messages to the client. Also handles periodic protocol-level pings.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close(websocket.StatusInternalError, "write pump closed")
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

func (c *Client) SendJSON(env core.Envelope) {
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
			c.Conn.Close(websocket.StatusPolicyViolation, "slow consumer")
			c.cancelFunc()
		}
	}
}
