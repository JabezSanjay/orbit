package auth

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "orbit-local-dev-secret-do-not-use-in-production"

func TestAuthenticateReturnsExpiry(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "alice",
		"exp": exp.Unix(),
	}).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/ws?token="+tok, nil)
	user, _, got, err := NewJWTAuthenticator(testSecret).Authenticate(r)
	if err != nil || user != "alice" {
		t.Fatalf("Authenticate = %q, %v", user, err)
	}
	if !got.Equal(exp) {
		t.Fatalf("expiresAt = %v, want %v", got, exp)
	}
}

func TestAuthenticateRejectsExpired(t *testing.T) {
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "alice",
		"exp": time.Now().Add(-time.Minute).Unix(),
	}).SignedString([]byte(testSecret))

	r := httptest.NewRequest("GET", "/ws?token="+tok, nil)
	if _, _, _, err := NewJWTAuthenticator(testSecret).Authenticate(r); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}
