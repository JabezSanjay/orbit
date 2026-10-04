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

	now func() time.Time // clock; overridable in tests
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
		now:          time.Now,
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
		b = &bucket{tokens: l.rate, lastRefil: l.now()}
		l.buckets[ip] = b
	}

	// Refill tokens based on elapsed time
	now := l.now()
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
//
// When trustedProxy is false (the default) only r.RemoteAddr is used: forwarding
// headers are client-controlled and would let anyone pick their own bucket.
//
// When trustedProxy is true, Orbit assumes it sits behind exactly one reverse
// proxy that it trusts:
//   - X-Real-IP is used if present. The proxy MUST overwrite (not pass through)
//     this header, e.g. nginx `proxy_set_header X-Real-IP $remote_addr;`.
//   - Otherwise the RIGHTMOST X-Forwarded-For entry is used: that is the address
//     the trusted proxy appended. Everything to its left was supplied by the
//     client and is ignored. If the rightmost entry is not a valid IP, the
//     header is ignored rather than falling back to client-supplied entries.
//   - If neither header yields an IP, r.RemoteAddr is used.
func (l *IPRateLimiter) clientIP(r *http.Request) string {
	if l.trustedProxy {
		if ip := parseIP(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		if ip := parseIP(rightmostForwarded(r.Header.Values("X-Forwarded-For"))); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rightmostForwarded returns the last non-empty entry across all
// X-Forwarded-For header lines (a proxy may append a new line instead of
// extending the existing one).
func rightmostForwarded(values []string) string {
	for i := len(values) - 1; i >= 0; i-- {
		parts := splitComma(values[i])
		for j := len(parts) - 1; j >= 0; j-- {
			if parts[j] != "" {
				return parts[j]
			}
		}
	}
	return ""
}

// parseIP normalises s to a canonical IP string, accepting an optional port
// ("1.2.3.4:5678", "[::1]:80"). Returns "" if s is not an IP.
func parseIP(s string) string {
	s = trimSpace(s)
	if s == "" {
		return ""
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	return ""
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
		cutoff := l.now().Add(-time.Minute)
		for ip, b := range l.buckets {
			if b.lastRefil.Before(cutoff) {
				delete(l.buckets, ip)
			}
		}
		l.mu.Unlock()
	}
}
