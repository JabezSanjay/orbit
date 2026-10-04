package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/orbit/orbit/internal/auth"
	"github.com/orbit/orbit/internal/core"
	"github.com/orbit/orbit/internal/presence"
	"github.com/orbit/orbit/internal/pubsub"
)

// apiHandlers holds the dependencies of the plain-HTTP API endpoints
// (everything except /ws and /metrics).
type apiHandlers struct {
	tracker         *presence.Tracker
	engine          pubsub.Engine
	authenticator   *auth.JWTAuthenticator
	serverSecret    string
	allowedOrigins  []string
	maxPublishBytes int64
}

// writeJSONError writes a JSON error body ({"ok":false,"error":msg}) with the given status.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": msg})
}

// writeJSON writes v as a JSON body with status 200.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// bearerToken returns the token from an "Authorization: Bearer <token>" header, or "".
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return h[len("Bearer "):]
	}
	return ""
}

// secretEqual compares two secrets in constant time. Both sides are hashed first
// so that the comparison does not leak the secret's length either.
func secretEqual(got, want string) bool {
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

// hasServerSecret reports whether the request carries Authorization: Bearer <ORBIT_SERVER_SECRET>.
func (a *apiHandlers) hasServerSecret(r *http.Request) bool {
	tok := bearerToken(r)
	return tok != "" && secretEqual(tok, a.serverSecret)
}

// clientPermissions validates a client JWT (Bearer header or ?token=) and returns
// its channel permissions. Kept as the single call site of Authenticate so that
// signature changes in internal/auth only need a one-line fix here.
func (a *apiHandlers) clientPermissions(r *http.Request) (*auth.ChannelPermissions, error) {
	_, perms, _, err := a.authenticator.Authenticate(r)
	return perms, err
}

// originAllowed reports whether origin matches ORBIT_ALLOWED_ORIGINS. A pattern
// containing "://" is matched against scheme://host; otherwise against the host
// only (the same form the WebSocket upgrade accepts). Patterns may use path.Match
// globs, e.g. "*.example.com".
func (a *apiHandlers) originAllowed(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	for _, p := range a.allowedOrigins {
		target := u.Host
		if strings.Contains(p, "://") {
			target = u.Scheme + "://" + u.Host
		}
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(target)); ok {
			return true
		}
	}
	return false
}

// applyCORS echoes the request Origin in Access-Control-Allow-Origin only when it
// is in ORBIT_ALLOWED_ORIGINS. Disallowed or missing origins get no CORS headers,
// so browsers block cross-origin reads.
func (a *apiHandlers) applyCORS(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if origin == "" || !a.originAllowed(origin) {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
}

// presenceGuard runs CORS, preflight, method, authentication and ACL checks
// shared by the presence endpoints. It returns the requested channel and true
// when the handler should proceed; otherwise it has already written a response.
//
// Access is granted to either the server secret (Authorization: Bearer
// <ORBIT_SERVER_SECRET>) or a valid client JWT whose subscribe ACL covers the
// channel.
func (a *apiHandlers) presenceGuard(w http.ResponseWriter, r *http.Request) (string, bool) {
	a.applyCORS(w, r)

	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return "", false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return "", false
	}

	var perms *auth.ChannelPermissions
	if !a.hasServerSecret(r) {
		p, err := a.clientPermissions(r)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return "", false
		}
		perms = p
	}

	channel := r.URL.Query().Get("channel")
	if channel == "" {
		writeJSONError(w, http.StatusBadRequest, "missing channel parameter")
		return "", false
	}

	if perms != nil && !auth.CanSubscribe(perms, channel) {
		writeJSONError(w, http.StatusForbidden, "forbidden")
		return "", false
	}
	return channel, true
}

// presence handles GET /api/presence?channel=<channel>.
func (a *apiHandlers) presence(w http.ResponseWriter, r *http.Request) {
	channel, ok := a.presenceGuard(w, r)
	if !ok {
		return
	}
	entries, err := a.tracker.GetUsersWithMetadata(r.Context(), channel)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, map[string]interface{}{
		"channel": channel,
		"users":   entries,
	})
}

// presenceCount handles GET /api/presence/count?channel=<channel>.
func (a *apiHandlers) presenceCount(w http.ResponseWriter, r *http.Request) {
	channel, ok := a.presenceGuard(w, r)
	if !ok {
		return
	}
	count, err := a.tracker.Count(r.Context(), channel)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, map[string]interface{}{
		"channel": channel,
		"count":   count,
	})
}

// publish handles POST /api/publish: lets backend services publish to a channel
// over HTTP. Authenticated by the server secret only; no channel ACL applies.
func (a *apiHandlers) publish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if !a.hasServerSecret(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeJSONError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, a.maxPublishBytes)
	var body struct {
		Channel string          `json:"channel"`
		Event   string          `json:"event"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Channel == "" {
		writeJSONError(w, http.StatusBadRequest, "channel is required")
		return
	}
	if core.IsReservedEvent(body.Event) {
		writeJSONError(w, http.StatusBadRequest, `event names starting with "presence." or "orbit." are reserved`)
		return
	}

	envelope, _ := json.Marshal(map[string]interface{}{
		"type":    "publish",
		"channel": body.Channel,
		"event":   body.Event,
		"payload": body.Payload,
	})
	if err := a.engine.Publish(r.Context(), body.Channel, envelope); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, map[string]interface{}{"ok": true})
}
