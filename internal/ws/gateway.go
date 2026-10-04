package ws

import (
	"context"
	"sync"

	"github.com/coder/websocket"
	"github.com/orbit/orbit/internal/metrics"
)

// Gateway manages the active local WebSocket connections.
type Gateway struct {
	Register   chan *Client
	Unregister chan *Client
	Clients    map[*Client]bool
	mu         sync.RWMutex

	maxConnsPerUser int
	userConns       map[string]int // userID -> active connection count

	wg   sync.WaitGroup // tracks registered connections; Done on Unregister
	done chan struct{}  // closed by Shutdown to stop Run()
	once sync.Once      // ensures done is closed exactly once
}

func NewGateway(maxConnsPerUser int) *Gateway {
	return &Gateway{
		Register:        make(chan *Client),
		Unregister:      make(chan *Client),
		Clients:         make(map[*Client]bool),
		maxConnsPerUser: maxConnsPerUser,
		userConns:       make(map[string]int),
		done:            make(chan struct{}),
	}
}

// AllowConnection returns true if the given userID may open another connection.
// A maxConnsPerUser of 0 means no cap (unlimited).
func (g *Gateway) AllowConnection(userID string) bool {
	if g.maxConnsPerUser == 0 {
		return true
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.userConns[userID] < g.maxConnsPerUser
}

func (g *Gateway) Run() {
	for {
		select {
		case <-g.done:
			return
		case client := <-g.Register:
			g.mu.Lock()
			g.Clients[client] = true
			g.userConns[client.UserID]++
			g.wg.Add(1)
			metrics.ActiveConnections.Inc()
			g.mu.Unlock()

		case client := <-g.Unregister:
			g.mu.Lock()
			if _, ok := g.Clients[client]; ok {
				delete(g.Clients, client)
				// client.Send is intentionally NOT closed: fanout workers may still
				// hold a reference and a send on a closed channel panics. WritePump
				// exits on the client's ctx instead.
				metrics.ActiveConnections.Dec()
				if g.userConns[client.UserID] > 0 {
					g.userConns[client.UserID]--
				}
				if g.userConns[client.UserID] == 0 {
					delete(g.userConns, client.UserID)
				}
				g.wg.Done()
			}
			g.mu.Unlock()
		}
	}
}

// unregister hands client to Run for removal. It never blocks forever: once
// Shutdown has stopped Run, the unregister is skipped.
func (g *Gateway) unregister(client *Client) {
	select {
	case g.Unregister <- client:
	case <-g.done:
	}
}

// Shutdown sends a clean WebSocket close frame to every connected client, waits for
// all connections to finish (or ctx to expire), then stops the Run() loop.
func (g *Gateway) Shutdown(ctx context.Context) {
	// Snapshot connected clients under read lock.
	g.mu.RLock()
	clients := make([]*Client, 0, len(g.Clients))
	for c := range g.Clients {
		clients = append(clients, c)
	}
	g.mu.RUnlock()

	// Send GoingAway close frame to each client. Client.Close is asynchronous,
	// so all handshakes run in parallel instead of one slow peer at a time.
	// Each ReadPump then errors out and unregisters.
	for _, c := range clients {
		c.Close(websocket.StatusGoingAway, "server shutting down")
	}

	// Wait for all Unregister events to be processed by Run() (wg.Done per client).
	waitDone := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-ctx.Done():
	}

	// Stop the Run() loop.
	g.once.Do(func() { close(g.done) })
}

func (g *Gateway) BroadcastLocal(channel string, env interface{}) {
	// Routing is handled by the router; left as extension point.
}
