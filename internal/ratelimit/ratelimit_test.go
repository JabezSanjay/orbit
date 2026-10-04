package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestLimiter builds a limiter with a controllable clock and no cleanup goroutine.
func newTestLimiter(rate int, trustedProxy bool, clock *time.Time) *IPRateLimiter {
	return &IPRateLimiter{
		buckets:      make(map[string]*bucket),
		rate:         rate,
		trustedProxy: trustedProxy,
		now:          func() time.Time { return *clock },
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		trusted    bool
		remoteAddr string
		realIP     string
		xff        []string // one entry per header line
		want       string
	}{
		{name: "untrusted uses RemoteAddr", remoteAddr: "10.0.0.1:1234", want: "10.0.0.1"},
		{name: "untrusted ignores X-Real-IP", remoteAddr: "10.0.0.1:1234", realIP: "1.1.1.1", want: "10.0.0.1"},
		{name: "untrusted ignores XFF", remoteAddr: "10.0.0.1:1234", xff: []string{"1.1.1.1"}, want: "10.0.0.1"},
		{name: "untrusted RemoteAddr without port", remoteAddr: "10.0.0.1", want: "10.0.0.1"},
		{name: "trusted prefers X-Real-IP", trusted: true, remoteAddr: "10.0.0.1:1234", realIP: "2.2.2.2", xff: []string{"3.3.3.3"}, want: "2.2.2.2"},
		{name: "trusted invalid X-Real-IP falls through to XFF", trusted: true, remoteAddr: "10.0.0.1:1234", realIP: "garbage", xff: []string{"3.3.3.3"}, want: "3.3.3.3"},
		{name: "trusted single XFF entry", trusted: true, remoteAddr: "10.0.0.1:1234", xff: []string{"3.3.3.3"}, want: "3.3.3.3"},
		{name: "trusted spoofed leftmost XFF is ignored", trusted: true, remoteAddr: "10.0.0.1:1234", xff: []string{"6.6.6.6, 7.7.7.7, 3.3.3.3"}, want: "3.3.3.3"},
		{name: "trusted XFF whitespace and trailing comma", trusted: true, remoteAddr: "10.0.0.1:1234", xff: []string{" 6.6.6.6 ,\t3.3.3.3 , "}, want: "3.3.3.3"},
		{name: "trusted XFF across multiple header lines uses last line", trusted: true, remoteAddr: "10.0.0.1:1234", xff: []string{"6.6.6.6", "3.3.3.3"}, want: "3.3.3.3"},
		{name: "trusted XFF entry with port", trusted: true, remoteAddr: "10.0.0.1:1234", xff: []string{"6.6.6.6, 3.3.3.3:5555"}, want: "3.3.3.3"},
		{name: "trusted XFF IPv6", trusted: true, remoteAddr: "10.0.0.1:1234", xff: []string{"6.6.6.6, 2001:DB8::1"}, want: "2001:db8::1"},
		{name: "trusted XFF bracketed IPv6 with port", trusted: true, remoteAddr: "10.0.0.1:1234", xff: []string{"[2001:db8::2]:443"}, want: "2001:db8::2"},
		{name: "trusted invalid rightmost XFF falls back to RemoteAddr", trusted: true, remoteAddr: "10.0.0.1:1234", xff: []string{"6.6.6.6, not-an-ip"}, want: "10.0.0.1"},
		{name: "trusted no headers uses RemoteAddr", trusted: true, remoteAddr: "[::1]:9999", want: "::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(0, 0)
			l := newTestLimiter(1, tt.trusted, &now)
			r := httptest.NewRequest(http.MethodGet, "/ws", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.realIP != "" {
				r.Header.Set("X-Real-IP", tt.realIP)
			}
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := l.clientIP(r); got != tt.want {
				t.Fatalf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAllowTokenBucket(t *testing.T) {
	type step struct {
		advance time.Duration
		ip      string
		want    bool
	}
	tests := []struct {
		name  string
		rate  int
		steps []step
	}{
		{
			name: "burst up to rate then deny",
			rate: 3,
			steps: []step{
				{ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: false},
			},
		},
		{
			name: "buckets are per IP",
			rate: 1,
			steps: []step{
				{ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: false},
				{ip: "2.2.2.2", want: true},
			},
		},
		{
			name: "refills over time",
			rate: 2,
			steps: []step{
				{ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: false},
				{advance: 500 * time.Millisecond, ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: false},
			},
		},
		{
			name: "refill is capped at rate",
			rate: 2,
			steps: []step{
				{ip: "1.1.1.1", want: true},
				{advance: time.Hour, ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: false},
			},
		},
		{
			name: "rate zero disables limiting",
			rate: 0,
			steps: []step{
				{ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: true},
				{ip: "1.1.1.1", want: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0)
			l := newTestLimiter(tt.rate, false, &now)
			for i, s := range tt.steps {
				now = now.Add(s.advance)
				r := httptest.NewRequest(http.MethodGet, "/ws", nil)
				r.RemoteAddr = s.ip + ":1234"
				if got := l.Allow(r); got != s.want {
					t.Fatalf("step %d (%s): Allow() = %v, want %v", i, s.ip, got, s.want)
				}
			}
		})
	}
}

func TestAllowSpoofedXFFSharesBucket(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newTestLimiter(1, true, &now)
	for i, spoof := range []string{"9.9.9.1", "9.9.9.2"} {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		r.Header.Set("X-Forwarded-For", spoof+", 3.3.3.3")
		want := i == 0
		if got := l.Allow(r); got != want {
			t.Fatalf("request %d with spoofed %s: Allow() = %v, want %v", i, spoof, got, want)
		}
	}
}

func TestAllowConcurrent(t *testing.T) {
	l := NewIPRateLimiter(1000, false)
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 100; i++ {
				r := httptest.NewRequest(http.MethodGet, "/ws", nil)
				r.RemoteAddr = "1.1.1.1:1"
				l.Allow(r)
			}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
}
