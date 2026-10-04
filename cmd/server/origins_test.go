package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/coder/websocket"
)

func TestParseAllowedOrigins(t *testing.T) {
	cors, hosts := parseAllowedOrigins(" http://localhost:5173/ , localhost:5173,https://*.example.com,,app.test:8443 ")
	wantCORS := []string{"http://localhost:5173", "localhost:5173", "https://*.example.com", "app.test:8443"}
	wantHosts := []string{"localhost:5173", "*.example.com", "app.test:8443"}
	if !reflect.DeepEqual(cors, wantCORS) {
		t.Errorf("cors = %q, want %q", cors, wantCORS)
	}
	if !reflect.DeepEqual(hosts, wantHosts) {
		t.Errorf("wsHosts = %q, want %q", hosts, wantHosts)
	}

	if cors, hosts := parseAllowedOrigins(""); cors != nil || hosts != nil {
		t.Errorf("empty input: got %q, %q", cors, hosts)
	}
}

// TestAllowedOriginsConsistent checks that both entry forms admit the same
// browser origin for the WebSocket upgrade (via coder/websocket) and for CORS.
func TestAllowedOriginsConsistent(t *testing.T) {
	const origin = "http://localhost:5173"
	for _, entry := range []string{"http://localhost:5173", "localhost:5173", "http://localhost:5173/"} {
		cors, hosts := parseAllowedOrigins(entry)

		a := &apiHandlers{allowedOrigins: cors}
		if !a.originAllowed(origin) {
			t.Errorf("%q: CORS rejected %s", entry, origin)
		}
		if a.originAllowed("http://evil.test:5173") {
			t.Errorf("%q: CORS accepted foreign origin", entry)
		}

		if code := upgradeStatus(t, hosts, origin); code != http.StatusSwitchingProtocols {
			t.Errorf("%q: WS upgrade from %s got %d, want 101", entry, origin, code)
		}
		if code := upgradeStatus(t, hosts, "http://evil.test:5173"); code != http.StatusForbidden {
			t.Errorf("%q: WS upgrade from foreign origin got %d, want 403", entry, code)
		}
	}
}

func upgradeStatus(t *testing.T, patterns []string, origin string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: patterns})
		if err == nil {
			c.CloseNow()
		}
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Origin", origin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upgrade request: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
