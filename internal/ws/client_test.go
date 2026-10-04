package ws

import (
	"context"
	"sync"
	"testing"

	"github.com/orbit/orbit/internal/core"
)

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
		c := NewClient("id", "user", nil, nil, g, nopRouter{}, 4, 0)
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
