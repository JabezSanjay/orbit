package ws

import (
	"context"
	"log"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/orbit/orbit/internal/auth"
	"github.com/orbit/orbit/internal/core"
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

	ctx        context.Context
	cancelFunc context.CancelFunc
}

func NewClient(id, userID string, perms *auth.ChannelPermissions, conn *websocket.Conn, gateway *Gateway, router Router) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		ID:          id,
		UserID:      userID,
		Permissions: perms,
		Conn:        conn,
		Send:        make(chan core.Envelope, 256),
		Gateway:     gateway,
		Router:      router,
		ctx:         ctx,
		cancelFunc:  cancel,
	}
}

// ReadPump handles messages arriving from the WebSocket connection.
func (c *Client) ReadPump() {
	defer func() {
		c.cancelFunc()
		c.Gateway.Unregister <- c
		c.Router.HandleDisconnect(context.Background(), c)
		c.Conn.CloseRead(context.Background())
	}()

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
			c.Send <- core.Envelope{Type: core.TypePong}
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
		case message, ok := <-c.Send:
			if !ok {
				// The hub closed the channel.
				c.Conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			
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
	case <-c.ctx.Done():
		return
	default: // Channel full, indicating slow client
		log.Printf("WARN: Slow client %s backpressure limit reached. Disconnecting user aggressively.", c.UserID)
		c.cancelFunc()
	}
}
