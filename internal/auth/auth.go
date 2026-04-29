package auth

import (
	"errors"
	"net/http"
	"strings"
	"github.com/golang-jwt/jwt/v5"
)

var ErrUnauthorized = errors.New("unauthorized")

// Authenticator validates connections and provides channel-level access control.
type Authenticator interface {
	Authenticate(r *http.Request) (string, error)
	CanSubscribe(userID, channel string) bool
	CanPublish(userID, channel string) bool
}

// JWTAuthenticator validates HS256-signed JWTs.
// The token must contain a non-empty "sub" claim and must not be expired.
// Token is read from Authorization: Bearer header first, then ?token= query param.
type JWTAuthenticator struct {
	secret []byte
}

// NewJWTAuthenticator creates a JWTAuthenticator.
// secret must be at least 32 bytes; callers should enforce this before calling.
func NewJWTAuthenticator(secret string) *JWTAuthenticator {
	return &JWTAuthenticator{secret: []byte(secret)}
}

func (a *JWTAuthenticator) Authenticate(r *http.Request) (string, error) {
	raw := extractToken(r)
	if raw == "" {
		return "", ErrUnauthorized
	}

	token, err := jwt.Parse(raw, func(t *jwt.Token) (interface{}, error) {
		// Reject any algorithm that is not HS256 — prevents alg:none and RS/ES attacks
		if t.Method != jwt.SigningMethodHS256 {
			return nil, ErrUnauthorized
		}
		return a.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())

	if err != nil || !token.Valid {
		return "", ErrUnauthorized
	}

	sub, err := token.Claims.GetSubject()
	if err != nil || sub == "" {
		return "", ErrUnauthorized
	}

	return sub, nil
}

func (a *JWTAuthenticator) CanSubscribe(userID, channel string) bool {
	return true // Channel-level ACLs implemented in #6
}

func (a *JWTAuthenticator) CanPublish(userID, channel string) bool {
	return true // Channel-level ACLs implemented in #6
}

// extractToken returns the raw JWT string from the request.
// Checks Authorization: Bearer header first, then ?token= query param.
func extractToken(r *http.Request) string {
	if header := r.Header.Get("Authorization"); header != "" {
		if strings.HasPrefix(header, "Bearer ") {
			return strings.TrimPrefix(header, "Bearer ")
		}
	}
	return r.URL.Query().Get("token")
}
