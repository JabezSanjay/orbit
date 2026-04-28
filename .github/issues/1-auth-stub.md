---
title: "feat: replace auth stub with real JWT / HMAC token validation"
labels: ["v0.1", "enhancement"]
---

## Summary

The current auth implementation is a stub. The `token` query parameter is mapped directly to a user ID (`"user_" + token`), with no actual validation.

## Current behaviour

```go
// internal/auth/auth.go
func (a *TokenAuthenticator) Authenticate(token string) (string, error) {
    if token == "" {
        return "anonymous", nil
    }
    return "user_" + token, nil
}
```

Any client that connects with `?token=anything` is granted full access.

## Required

Replace `TokenAuthenticator` with a real implementation:

- **Option A — HMAC-signed tokens:** Validate a shared-secret signature on the token (suitable for server-generated tokens)
- **Option B — JWT:** Decode and verify a standard JWT (RS256 or HS256); extract `sub` as userID

The `Authenticator` interface in `internal/auth/auth.go` is already in place — only the implementation needs replacing.

## Acceptance criteria

- [ ] Token with invalid signature → WebSocket upgrade rejected with `4001`
- [ ] Expired token → rejected with `4001`
- [ ] Valid token → userID extracted from `sub` claim (JWT) or payload (HMAC)
- [ ] Empty/missing token → rejected (remove anonymous fallback)
- [ ] Hardcoded secret `"secret"` removed from `cmd/server/main.go`; loaded from env var

## Roadmap

v0.1 — Trustworthy
