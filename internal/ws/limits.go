package ws

import "time"

// DefaultMaxMessageBytes is the inbound frame size limit used when
// ClientLimits.MaxMessageBytes is not positive.
const DefaultMaxMessageBytes = 64 * 1024

// ClientLimits are the per-connection limits, configured once at startup.
type ClientLimits struct {
	// BufferSize is the outbound queue length (ORBIT_CLIENT_BUFFER_SIZE).
	BufferSize int
	// SlowThreshold is the number of consecutive outbound drops before the
	// client is closed with 1008 (ORBIT_SLOW_CLIENT_THRESHOLD); 0 disables.
	SlowThreshold int
	// MaxMessageBytes caps a single inbound frame (ORBIT_MAX_MESSAGE_BYTES).
	// Larger frames make the websocket library close the connection with
	// 1009 Message Too Big. <= 0 means DefaultMaxMessageBytes.
	MaxMessageBytes int64
	// MessagesPerSec is the sustained inbound frame rate per connection
	// (ORBIT_CLIENT_MSGS_PER_SEC), with a burst of twice that; 0 disables.
	// Ping frames count against it.
	MessagesPerSec int
}

// abuseDuration is how long a client must stay over its inbound limit —
// with at least one dropped frame in every one-second window — before the
// connection is closed with 1008.
const abuseDuration = 3 * time.Second

// inboundLimiter is a per-connection token bucket for client frames, plus
// tracking of sustained abuse. It is only used from the ReadPump goroutine,
// so it needs no locking.
type inboundLimiter struct {
	rate   float64 // tokens added per second
	burst  float64 // bucket capacity
	tokens float64
	last   time.Time

	start       time.Time // origin for one-second window numbering
	lastDropW   int64     // window of the most recent drop (-1: none yet)
	streakStart time.Time // first drop of the current run of consecutive windows
	lastErrW    int64     // window of the most recent error reply (-1: none yet)
}

// newInboundLimiter returns nil (no limiting) when perSec <= 0.
func newInboundLimiter(perSec int, now time.Time) *inboundLimiter {
	if perSec <= 0 {
		return nil
	}
	burst := float64(2 * perSec)
	return &inboundLimiter{
		rate:      float64(perSec),
		burst:     burst,
		tokens:    burst,
		last:      now,
		start:     now,
		lastDropW: -1,
		lastErrW:  -1,
	}
}

// allow consumes a token for a frame arriving at now and reports whether the
// frame may be processed.
func (l *inboundLimiter) allow(now time.Time) bool {
	if elapsed := now.Sub(l.last).Seconds(); elapsed > 0 {
		l.tokens += elapsed * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
	}
	l.last = now
	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}

// dropped records a rate-limited frame at now. notify is true at most once
// per one-second window, so error replies are themselves rate limited.
// abusive is true once drops have occurred in every one-second window for at
// least abuseDuration.
func (l *inboundLimiter) dropped(now time.Time) (notify, abusive bool) {
	w := int64(now.Sub(l.start) / time.Second)
	switch {
	case w == l.lastDropW:
		// same window, streak unchanged
	case l.lastDropW >= 0 && w == l.lastDropW+1:
		// next consecutive window, streak continues
	default:
		l.streakStart = now
	}
	l.lastDropW = w

	if w != l.lastErrW {
		l.lastErrW = w
		notify = true
	}
	return notify, now.Sub(l.streakStart) >= abuseDuration
}
