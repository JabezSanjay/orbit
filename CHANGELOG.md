# Changelog

All notable changes to Orbit will be documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Phase 1 — Stable Alpha (upcoming)
- JWT / HMAC token authentication replacing the current stub
- Channel-level ACLs (`CanSubscribe` / `CanPublish`)
- Remove `InsecureSkipVerify` from WebSocket accept
- JS SDK server-side `unsubscribe` frame

### Phase 2 — Public Beta (planned)
- Message history and replay via Redis Streams
- Per-namespace channel configuration (TTL, ACL, history depth)
- Connection rate limiting and per-user caps
- Python SDK and Go client SDK

### Phase 3 — v1.0 (planned)
- Read-only admin dashboard
- Official Docker Hub image + versioned releases
- Helm chart for Kubernetes
- Structured JSON logging
- Graceful shutdown with in-flight message draining

### Phase 4 — Ecosystem (future)
- REST publish API
- Webhooks on publish / presence events
- SSE fallback transport
- Multi-tenant namespace isolation
- Official Grafana dashboard

---

## [0.1.0] — 2026-04-28

### Added
- WebSocket server with JSON envelope protocol (`subscribe`, `unsubscribe`, `publish`, `ping`, `pong`, `message`)
- Redis PubSub engine with multiplexed connection and FNV-hashed worker pool dispatch (default 100 workers)
- Presence tracking via Redis Sorted Sets with 45-second TTL
- `presence.joined` and `presence.left` system events
- `/api/presence?channel=` HTTP endpoint to query active users
- Prometheus metrics: publish latency, fanout latency, active connections, active subscriptions, reconnects, dropped messages
- Exponential backoff with jitter on Redis reconnect
- JavaScript SDK (`sdk/js/orbit.js`) — auto-reconnect, auto-resubscribe, heartbeat ping
- Extended SDK (`example/src/orbit.js`) — adds `disconnect()` and `intentionalClose` flag
- React + Vite live cursor demo app (`example/`) — LERP cursor smoothing, latency measurement, presence display
- Multi-stage Dockerfile and Docker Compose setup
- Benchmark tool (`cmd/bench`) — 10k channel stress test with pprof + Prometheus
- Integration test scripts (`tests/ws-pubsub.test.js`, `tests/sdk-presence.test.js`)
- `ORBIT_FANOUT_WORKERS` environment variable for tuning worker pool size
