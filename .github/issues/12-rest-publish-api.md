---
title: "feat: REST publish API — publish to a channel over HTTP"
labels: ["v0.2", "enhancement"]
---

## Summary

Publishing currently requires a live WebSocket connection. Backend services (cron jobs, payment webhooks, worker queues) cannot easily integrate with Orbit because they don't hold persistent connections. A REST publish endpoint unlocks server-side integrations without any SDK dependency.

## Required

```
POST /api/publish
Content-Type: application/json
Authorization: Bearer <server-secret>

{
  "channel": "notifications",
  "event":   "payment.completed",
  "payload": { "amount": 99.00, "currency": "USD" }
}
```

Response (success):
```json
{ "ok": true }
```

Response (error):
```json
{ "ok": false, "error": "channel is required" }
```

## Authentication

The REST endpoint must be protected. Use a **shared server secret** distinct from client tokens:

- Env var: `ORBIT_SERVER_SECRET`
- Header: `Authorization: Bearer <secret>`
- Missing or invalid secret → `401 Unauthorized`
- This secret is never sent to WebSocket clients

## Behaviour

- The message is published into the Redis PubSub channel, identical to a `publish` envelope from a WebSocket client
- All subscribers on all nodes receive the message as a `type: "message"` envelope
- The HTTP request returns as soon as the message is handed to Redis (fire-and-forget from the caller's perspective)

## Acceptance criteria

- [ ] `POST /api/publish` publishes a message to the specified channel
- [ ] Message is received by all WebSocket subscribers on all nodes
- [ ] Missing `Authorization` header or wrong secret → `401`
- [ ] Missing `channel` field → `400 Bad Request`
- [ ] `payload` is optional; omitting it is valid
- [ ] `ORBIT_SERVER_SECRET` env var is required; server refuses to start if unset (to prevent accidental open endpoints)
- [ ] Endpoint documented in README HTTP endpoints table
- [ ] Integration test: publish via HTTP, verify message received on a WebSocket subscriber

## Notes

- Reuse the existing `pubsub.Engine.Publish()` method — no new Redis path needed
- Do not share the client auth secret with this endpoint; they serve different trust models
- Rate limiting for this endpoint is a separate concern (not in scope for this issue)

## Roadmap

v0.2 — Presence Engine
