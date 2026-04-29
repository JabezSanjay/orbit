package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// IPRateLimiter enforces a per-IP token bucket rate limit on new connections.
// Each IP gets a bucket of `rate` tokens that refills at `rate` tokens/second.
// When the bucket is empty the request is rejected with 429.
type IPRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    int // tokens per second (= max burst too)

	trustedProxy bool // whether to read X-Forwarded-For / X-Real-IP
}

type bucket struct {
	tokens    int
	lastRefil time.Time
}

// NewIPRateLimiter creates a new rate limiter.
// rate is the maximum number of new connections allowed per second per IP.
// trustedProxy controls whether X-Forwarded-For / X-Real-IP headers are trusted.
func NewIPRateLimiter(rate int, trustedProxy bool) *IPRateLimiter {
	l := &IPRateLimiter{
		buckets:      make(map[string]*bucket),
		rate:         rate,
		trustedProxy: trustedProxy,
	}
	// Background cleanup: remove stale buckets every minute
	go l.cleanup()
	return l
}

// Allow returns true if the request from the given IP is within the rate limit.
// If the limiter was created with rate=0, all requests are allowed (disabled).
func (l *IPRateLimiter) Allow(r *http.Request) bool {
	if l.rate == 0 {
		return true
	}
	ip := l.clientIP(r)

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.rate, lastRefil: time.Now()}
		l.buckets[ip] = b
	}

	// Refill tokens based on elapsed time
	now := time.Now()
	elapsed := now.Sub(b.lastRefil).Seconds()
	refill := int(elapsed * float64(l.rate))
	if refill > 0 {
		b.tokens += refill
		if b.tokens > l.rate {
			b.tokens = l.rate
		}
		b.lastRefil = now
	}

	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// clientIP extracts the real client IP from the request.
// When trustedProxy is true, checks X-Real-IP then X-Forwarded-For first.
func (l *IPRateLimiter) clientIP(r *http.Request) string {
	if l.trustedProxy {
		if ip := r.Header.Get("X-Real-IP"); ip != "" {
			if parsed := net.ParseIP(ip); parsed != nil {
				return parsed.String()
			}
		}
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			// X-Forwarded-For can be a comma-separated list; take the first (client) IP
			for _, part := range splitComma(fwd) {
				if parsed := net.ParseIP(part); parsed != nil {
					return parsed.String()
				}
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, trimSpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, trimSpace(s[start:]))
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// cleanup removes buckets that haven't been used for over a minute.
func (l *IPRateLimiter) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		cutoff := time.Now().Add(-time.Minute)
		for ip, b := range l.buckets {
			if b.lastRefil.Before(cutoff) {
				delete(l.buckets, ip)
			}
		}
		l.mu.Unlock()
	}
}
