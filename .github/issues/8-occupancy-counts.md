---
title: "feat: occupancy counts per channel (live member count API)"
labels: ["v0.2", "enhancement", "presence"]
---

## Summary

There is no way to ask Orbit "how many people are in this channel right now?" without fetching the full user list and counting it yourself. A dedicated occupancy API makes this a first-class primitive.

## Current behaviour

The existing `/api/presence?channel=<channel>` endpoint returns the full array of user IDs. Clients must `.length` the array to get a count — there is no lightweight count-only path.

```go
// internal/presence/tracker.go
func (t *Tracker) Count(ctx context.Context, channel string) (int64, error)
```

`Count` exists internally but is not exposed over HTTP.

## Required

**1. HTTP endpoint**

```
GET /api/presence/count?channel=<channel>
```

Response:
```json
{ "channel": "live-canvas", "count": 42 }
```

**2. WebSocket push (optional, stretch)**

After a `subscribe`, the server may push a `presence.count` event when the occupancy changes, so UIs can display a live count without polling.

## Acceptance criteria

- [ ] `GET /api/presence/count?channel=x` returns `{ "channel": "x", "count": N }`
- [ ] Count reflects TTL-expired members correctly (expired users not counted)
- [ ] Missing `channel` param → `400 Bad Request`
- [ ] Endpoint documented in README HTTP endpoints table

## Notes

- Reuse `presence.Tracker.Count()` — the Redis `ZCOUNT` call is already implemented
- This is a read-only endpoint; no writes, no auth required for now

## Roadmap

v0.2 — Presence Engine
