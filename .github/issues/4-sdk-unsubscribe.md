---
title: "fix: JS SDK unsubscribe() should send unsubscribe frame to server"
labels: ["v0.1", "bug"]
github_issue: 7
---

## Summary

Calling `orbit.unsubscribe(channel, handler)` in the JS SDK removes the local handler but never sends an `unsubscribe` frame to the server. The server continues to forward messages for that channel to the connection, wasting bandwidth.

## Current behaviour

```js
// sdk/js/orbit.js
unsubscribe(channel, handler) {
    // removes local handler only — no server frame sent
    const handlers = this.subscriptions.get(channel) || [];
    this.subscriptions.set(channel, handlers.filter(h => h !== handler));
}
```

## Required

When the last handler for a channel is removed, send:

```json
{ "type": "unsubscribe", "channel": "<channel>" }
```

Only send the frame when the handler count for that channel drops to zero — not on every `unsubscribe()` call (a channel may have multiple handlers).

Also ensure the channel is removed from `this.subscriptions` (and the reconnect re-subscribe list) when the count reaches zero.

## Acceptance criteria

- [ ] Last handler removed → server receives `{"type":"unsubscribe","channel":"..."}` 
- [ ] Non-last handler removed → no frame sent
- [ ] After unsubscribe, reconnect does not re-subscribe the channel
- [ ] Test in `tests/sdk-presence.test.js` or a new integration test

## Roadmap

v0.1 — Trustworthy
