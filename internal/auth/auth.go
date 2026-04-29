package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

var ErrUnauthorized = errors.New("unauthorized")

// ChannelPermissions holds the channel patterns a connection is allowed to use.
// Parsed from the JWT "channels" claim at connect time and stored on the Client.
// An empty slice means no access. Use ["*"] to allow all channels.
type ChannelPermissions struct {
	Subscribe []string
	Publish   []string
}

// CanSubscribe returns true if the given channel matches any pattern in perms.Subscribe.
func CanSubscribe(perms *ChannelPermissions, channel string) bool {
	if perms == nil {
		return false
	}
	return matchAny(perms.Subscribe, channel)
}

// CanPublish returns true if the given channel matches any pattern in perms.Publish.
func CanPublish(perms *ChannelPermissions, channel string) bool {
	if perms == nil {
		return false
	}
	return matchAny(perms.Publish, channel)
}

// matchAny returns true if channel matches at least one pattern.
// Supports a single trailing wildcard: "room-*" matches "room-anything".
// "*" alone matches any channel.
func matchAny(patterns []string, channel string) bool {
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if strings.HasSuffix(p, "*") {
			if strings.HasPrefix(channel, p[:len(p)-1]) {
				return true
			}
		} else if p == channel {
			return true
		}
	}
	return false
}

// Authenticator validates connections and returns the userID and channel permissions.
type Authenticator interface {
	Authenticate(r *http.Request) (userID string, perms *ChannelPermissions, err error)
}

// JWTAuthenticator validates HS256-signed JWTs.
// The token must contain a non-empty "sub" claim and must not be expired.
// Token is read from Authorization: Bearer header first, then ?token= query param.
// Channel permissions are read from the "channels" claim:
//
//	{
//	  "sub": "user123",
//	  "exp": 1234567890,
//	  "channels": {
//	    "subscribe": ["room-*", "notifications-user123"],
//	    "publish":   ["room-1"]
//	  }
//	}
//
// If the "channels" claim is absent or either list is empty, access is denied (deny by default).
// Use ["*"] to allow all channels.
type JWTAuthenticator struct {
	secret []byte
}

// NewJWTAuthenticator creates a JWTAuthenticator.
// secret must be at least 32 bytes; callers should enforce this before calling.
func NewJWTAuthenticator(secret string) *JWTAuthenticator {
	return &JWTAuthenticator{secret: []byte(secret)}
}

func (a *JWTAuthenticator) Authenticate(r *http.Request) (string, *ChannelPermissions, error) {
	raw := extractToken(r)
	if raw == "" {
		return "", nil, ErrUnauthorized
	}

	token, err := jwt.Parse(raw, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, ErrUnauthorized
		}
		return a.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())

	if err != nil || !token.Valid {
		return "", nil, ErrUnauthorized
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", nil, ErrUnauthorized
	}

	sub, err := claims.GetSubject()
	if err != nil || sub == "" {
		return "", nil, ErrUnauthorized
	}

	perms := parseChannelPermissions(claims)
	return sub, perms, nil
}

func parseChannelPermissions(claims jwt.MapClaims) *ChannelPermissions {
	perms := &ChannelPermissions{}
	channelsClaim, ok := claims["channels"]
	if !ok {
		return perms
	}
	channelsMap, ok := channelsClaim.(map[string]interface{})
	if !ok {
		return perms
	}
	perms.Subscribe = extractStringSlice(channelsMap, "subscribe")
	perms.Publish = extractStringSlice(channelsMap, "publish")
	return perms
}

func extractStringSlice(m map[string]interface{}, key string) []string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			result = append(result, s)
		}
	}
	return result
}

func extractToken(r *http.Request) string {
	if header := r.Header.Get("Authorization"); header != "" {
		if strings.HasPrefix(header, "Bearer ") {
			return strings.TrimPrefix(header, "Bearer ")
		}
	}
	return r.URL.Query().Get("token")
}
