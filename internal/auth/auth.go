package auth

import (
	"errors"
	"net/http"
)

var ErrUnauthorized = errors.New("unauthorized")

// Authenticator validates connections and provides channel-level access control.
type Authenticator interface {
	Authenticate(r *http.Request) (string, error)
	CanSubscribe(userID, channel string) bool
	CanPublish(userID, channel string) bool
}

// TokenAuthenticator is a basic authenticator that checks for a 'token' query param.
// In a real system, you'd decode a JWT or verify against an auth service.
type TokenAuthenticator struct {
	secret string // Example for future extension
}

func NewTokenAuthenticator(secret string) *TokenAuthenticator {
	return &TokenAuthenticator{secret: secret}
}

func (a *TokenAuthenticator) Authenticate(r *http.Request) (string, error) {
	token := r.URL.Query().Get("token")
	if token == "" {
		// MVP: If no token, return empty string, let's treat them as anonymous.
		return "anonymous", nil
	}
	
	// Mock validation: In reality, decode and map to a userID
	userID := "user_" + token
	return userID, nil
}

func (a *TokenAuthenticator) CanSubscribe(userID, channel string) bool {
	// MVP: Everyone can subscribe to everything
	return true
}

func (a *TokenAuthenticator) CanPublish(userID, channel string) bool {
	// MVP: Everyone can publish to everything
	return true
}
