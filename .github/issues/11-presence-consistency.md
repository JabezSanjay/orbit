---
title: "feat: presence consistency — harden edge cases on unclean disconnect, duplicate joins, and Redis blip"
labels: ["v0.2", "enhancement", "presence", "reliability"]
---

## Summary

Presence is only useful if it's accurate. The current implementation has several known edge cases that can cause ghost users (shown as online when offline), missed events, or duplicate `presence.joined` fires. This issue documents and fixes them.

## Known edge cases

### 1. Unclean disconnect (Wi-Fi drop, tab close)
A client that loses connectivity without sending a close frame keeps its presence entry alive until TTL expiry (45s). During that window it appears online but cannot receive messages.

**Expected:** No change to this behaviour — TTL-based expiry is the intended design. **Document this explicitly** so app developers know to account for it (e.g. show "away" state after a shorter timeout client-side).

### 2. Duplicate `presence.joined` on reconnect
A client reconnects within the TTL window and re-subscribes. The server fires `presence.joined` again even though the user was never considered offline from the channel's perspective.

**Expected:** If the user is already present (exists in the sorted set), suppress `presence.joined` or emit `presence.rejoined` (TBD — pick one, document it).

### 3. `presence.left` race on rapid reconnect
A client disconnects and reconnects faster than the disconnect handler propagates. `presence.left` fires, then `presence.joined` fires. Subscribers briefly see the user as offline.

**Expected:** Introduce a short grace period (e.g. 2s) before broadcasting `presence.left`. If the user rejoins within the grace period, suppress the `left` event entirely.

### 4. Duplicate subscriptions from same client
A client sends two `subscribe` frames for the same channel without an intervening `unsubscribe`. Currently this is silently ignored or double-registers the handler.

**Expected:** Idempotent subscribe — second subscribe for same channel is a no-op.

### 5. Redis blip — presence state after reconnect
If Redis restarts, all sorted sets are lost. Clients that were subscribed receive no `presence.left` events for their peers.

**Expected:** Document the recovery path. On Redis reconnect, the node should re-subscribe to all channels (already implemented for PubSub). Presence entries will be missing until clients send their next `ping`. This is acceptable — document it.

### 6. Cross-node `presence.left` on node shutdown
When an Orbit node shuts down gracefully, it disconnects all clients. The clients reconnect to another node. However, the old node's disconnect handlers fire `presence.left` events that may race with `presence.joined` from the new node.

**Expected:** Graceful shutdown should delay firing `presence.left` events until client reconnect window has elapsed, or publish a `presence.node_shutdown` event that downstream clients can interpret.

## Acceptance criteria

- [ ] Behaviour for each of the 6 cases above is **documented** (even if the decision is "this is acceptable, here's why")
- [ ] Duplicate `presence.joined` on reconnect within TTL window is suppressed or replaced with `presence.rejoined`
- [ ] Idempotent `subscribe` — sending twice does not double-emit `presence.joined`
- [ ] Grace period for `presence.left` is implemented (configurable via `ORBIT_PRESENCE_LEAVE_GRACE_MS`, default 2000ms)
- [ ] Behaviour under Redis restart is documented in `AGENTS.md`
- [ ] Integration tests cover: duplicate subscribe, reconnect within TTL window, rapid disconnect+reconnect

## Roadmap

v0.2 — Presence Engine
