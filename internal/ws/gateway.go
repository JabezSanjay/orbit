package ws

import (
	"sync"
	"sync/atomic"
)

// Gateway manages the active local WebSocket connections.
type Gateway struct {
	Register   chan *Client
	Unregister chan *Client
	Clients    map[*Client]bool
	mu         sync.RWMutex
	Metrics    *Metrics
}

type Metrics struct {
	ActiveConnections int64
}

func NewGateway() *Gateway {
	return &Gateway{
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
		Clients:    make(map[*Client]bool),
		Metrics:    &Metrics{},
	}
}

func (g *Gateway) Run() {
	for {
		select {
		case client := <-g.Register:
			g.mu.Lock()
			g.Clients[client] = true
			atomic.AddInt64(&g.Metrics.ActiveConnections, 1)
			g.mu.Unlock()

		case client := <-g.Unregister:
			g.mu.Lock()
			if _, ok := g.Clients[client]; ok {
				delete(g.Clients, client)
				close(client.Send)
				atomic.AddInt64(&g.Metrics.ActiveConnections, -1)
			}
			g.mu.Unlock()
		}
	}
}

func (g *Gateway) BroadcastLocal(channel string, env interface{}) {
	// If we map clients to channels in Gateway we can target them, 
	// but for now router will fan out, or we can look up clients by subscription here.
	// We'll leave this to the router for complexity separation.
}
