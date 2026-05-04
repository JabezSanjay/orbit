---
title: "feat: room metadata — attach arbitrary state to a presence entry"
labels: ["v0.2", "enhancement", "presence"]
---

## Summary

Presence entries currently store only a `userID`. Real multiplayer apps need to attach context to each user: their avatar URL, display name, cursor colour, or current page. Without this, clients must maintain a separate side-channel for user state.

## Current behaviour

The Redis sorted set stores `userID` as the member with a TTL score. There is no way to attach additional data to a presence entry.

```go
// internal/presence/tracker.go
func (t *Tracker) Add(ctx context.Context, channel, userID string) error
```

## Required

**1. Metadata storage**

Allow clients to attach a `metadata` object when subscribing:

```json
{
  "type": "subscribe",
  "channel": "my-channel",
  "metadata": {
    "name": "Alice",
    "avatar": "https://example.com/alice.png",
    "color": "#e74c3c"
  }
}
```

**2. Storage backend**

Store metadata as a Redis Hash alongside the sorted set:

```
Key:   orbit:presence:meta:<channel>:<userID>
Type:  Hash (field → value)
TTL:   Same as presence TTL
```

**3. Retrieval**

The `/api/presence?channel=<channel>` response should expand to include metadata:

```json
[
  { "user": "user_alice", "metadata": { "name": "Alice", "color": "#e74c3c" } },
  { "user": "user_bob",   "metadata": { "name": "Bob",   "color": "#3498db" } }
]
```

**4. Presence events**

`presence.joined` payload should include metadata:

```json
{ "type": "message", "event": "presence.joined", "payload": { "user": "user_alice", "metadata": { "name": "Alice" } } }
```

## Acceptance criteria

- [ ] `subscribe` envelope accepts optional `metadata` object (max 2 KB serialised)
- [ ] Metadata stored in Redis and TTL-refreshed alongside the presence entry
- [ ] `/api/presence` returns `[{ user, metadata }]` objects (backwards-compatible: `metadata` is `{}` if not set)
- [ ] `presence.joined` event includes `metadata`
- [ ] `presence.left` event includes `user` only (metadata already gone or irrelevant)
- [ ] Metadata exceeding 2 KB → `error` envelope; subscription still proceeds without metadata
- [ ] On disconnect, metadata key is deleted alongside presence removal

## Notes

- Keep the metadata schema open (`map[string]any`) — Orbit should not impose structure on what apps store
- Do not index or query metadata server-side; it is opaque to Orbit

## Roadmap

v0.2 — Presence Engine
