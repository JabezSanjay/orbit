---
title: "feat: graceful shutdown with in-flight message draining"
labels: ["v0.1", "enhancement"]
---

## Summary

When the server receives `SIGTERM` or `SIGINT`, it exits immediately. In-flight messages are dropped and connected clients receive an ungraceful close.

## Required

On shutdown signal:

1. Stop accepting new WebSocket connections (return `503` on `/ws`)
2. Stop accepting new Redis PubSub subscriptions
3. Allow the fanout worker pool to drain pending messages (up to a configurable deadline, e.g. `ORBIT_SHUTDOWN_TIMEOUT`, default `10s`)
4. Send WebSocket close frames to all connected clients
5. Wait for all client write pumps to flush, then exit

## Acceptance criteria

- [ ] `SIGTERM` → server stops accepting new connections
- [ ] In-flight messages in worker queues are delivered before exit (within deadline)
- [ ] All connected clients receive a clean WebSocket close frame
- [ ] Process exits with code `0` on clean shutdown
- [ ] `ORBIT_SHUTDOWN_TIMEOUT` env var documented in README

## Roadmap

v0.1 — Trustworthy
