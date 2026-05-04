---
title: "feat: configurable presence TTLs per channel"
labels: ["v0.2", "enhancement", "presence"]
---

## Summary

Presence TTL is hardcoded at 45 seconds globally. Different channels have different requirements — a live-cursor canvas needs a short TTL (fast offline detection), while a document editor may tolerate a longer one to avoid flicker during brief network interruptions.

## Current behaviour

```go
// internal/presence/tracker.go
const presenceTTL = 45 * time.Second
```

Every channel uses the same TTL with no way to override it.

## Required

Make the TTL configurable at two levels:

**1. Global default via env var**

```
ORBIT_PRESENCE_TTL_SECONDS=45
```

Replaces the hardcoded constant. Defaults to `45` if unset.

**2. Per-channel TTL (stretch)**

Allow a `ttl` field in the `subscribe` envelope:

```json
{ "type": "subscribe", "channel": "my-channel", "ttl": 120 }
```

The server stores the TTL alongside the subscription and uses it for all subsequent `Add` / refresh calls on that channel for this client.

## Acceptance criteria

- [ ] `ORBIT_PRESENCE_TTL_SECONDS` env var sets the global default
- [ ] Default remains 45s when env var is absent
- [ ] Per-channel TTL from `subscribe` envelope is honoured (stretch)
- [ ] Per-channel TTL is validated: must be between 5s and 3600s; out-of-range → `error` envelope back to client
- [ ] TTL documented in README and AGENTS.md presence section

## Notes

- The `Tracker` already accepts TTL as a parameter in `Add()`; the change is primarily in how that value is sourced
- The `2×TTL` key expiry on the Redis sorted set must be updated to track the per-channel TTL as well

## Roadmap

v0.2 — Presence Engine
