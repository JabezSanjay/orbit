# Changelog

All notable changes to Orbit will be documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### v0.1 — Trustworthy (upcoming)
- JWT / HMAC token authentication replacing the current stub
- Channel-level ACLs (`CanSubscribe` / `CanPublish`)
- Remove `InsecureSkipVerify` from WebSocket accept
- JS SDK server-side `unsubscribe` frame
- Connection rate limiting and per-user connection caps
- Graceful shutdown with in-flight message draining
- Slow consumer detection — per-connection outbound buffer limits and drop policy

### v0.2 — Presence Engine (planned)
- Occupancy counts per channel (live member count API)
- Configurable presence TTLs per channel
- Room metadata — attach arbitrary state to a presence entry
- Presence consistency semantics (unclean disconnect, partition, Redis blip)
- REST publish API — publish over HTTP without a WebSocket connection
- Load tests + published benchmarks (10k, 50k, 100k connections)

### v0.3 — Adoption (planned)
- "Build live cursors in 5 minutes" quickstart
- Next.js + React integration examples
- Go backend integration example
- "Migrate from Pusher" guide
- `BENCHMARK.md` — real numbers on a $5 VPS

### v0.5 — Observable (planned)
- Message history and replay via Redis Streams
- Per-namespace channel configuration (TTL, ACL, history depth)
- TLS termination guide
- Structured JSON logging
- Official Grafana dashboard (pre-built, importable)
- Official Docker Hub image + versioned releases

### v1.0 — Platform (future)
- Chaos testing — Redis kill, node restart, network drop, channel flood
- Go client SDK
- Python SDK
- Webhooks on publish / presence events
- Helm chart for Kubernetes
- Protocol compatibility guarantees — SemVer policy for wire protocol and SDK breaking changes

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
