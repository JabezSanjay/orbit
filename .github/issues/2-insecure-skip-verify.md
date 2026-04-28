---
title: "fix: remove InsecureSkipVerify from WebSocket accept"
labels: ["v0.1", "bug", "security"]
github_issue: 5
---

## Summary

`InsecureSkipVerify: true` is set on the WebSocket accept options in `cmd/server/main.go`. This disables origin-check enforcement and must be removed before any public deployment.

## Current behaviour

```go
conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
    InsecureSkipVerify: true,
})
```

All cross-origin WebSocket connections are accepted without validation.

## Required

- Remove `InsecureSkipVerify: true`
- Replace with an explicit `OriginPatterns` allowlist loaded from an env var (e.g. `ORBIT_ALLOWED_ORIGINS`)
- Default to same-origin only when the env var is not set
- Document the env var in README

## Acceptance criteria

- [ ] Cross-origin connection from an unlisted origin → rejected with `403`
- [ ] Connection from an allowed origin → accepted
- [ ] `InsecureSkipVerify` is gone from the codebase
- [ ] `ORBIT_ALLOWED_ORIGINS` documented in README env table

## Roadmap

v0.1 — Trustworthy
