# Changelog

All notable changes to Orbit will be documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Fixed / Security
- Fix `send on closed channel` panic when a fanout worker delivered to a client during disconnect — clients are removed from the router before unregistering and the outbound buffer is never closed; `pong` replies no longer block the read loop
- Slow-consumer disconnects no longer stall fanout — the 1008 close handshake runs asynchronously (once per connection) instead of blocking the fanout worker while it holds the router lock; shutdown close frames are sent in parallel
- Router no longer holds its global lock across Redis I/O — subscribe/disconnect update in-memory tables under the lock and do presence/publish/(un)subscribe afterwards; disconnect only visits the client's own channels instead of every channel on the node. Publishing to a channel now refreshes presence only if the connection is subscribed to it
- Prevent event spoofing — clients can no longer publish `presence.*` or `orbit.*` events (error frame instead); relayed publishes are rebuilt server-side (client `ttl`/`metadata` stripped) and carry a server-stamped `sender` (publisher's userID; absent on REST publishes); presence payloads are JSON-encoded instead of string-concatenated
- Enforce JWT expiry on live connections — `exp` was only checked at connect; the server now closes the connection with close code `4001` (`token expired`) when the token expires. `Authenticator.Authenticate` also returns the expiry
- Per-connection inbound limits — max frame size `ORBIT_MAX_MESSAGE_BYTES` (default 65536, was a hardcoded 1 MB; oversized frames close with 1009) and inbound rate limit `ORBIT_CLIENT_MSGS_PER_SEC` (default 100/s, burst 2×, `0` disables): excess frames are dropped with a rate-limited `error` frame, sustained floods (3 consecutive seconds) close with 1008; new metric `orbit_inbound_rate_limited_total`
- `POST /api/publish` rejects reserved `presence.*` / `orbit.*` events with `400`, so backends cannot spoof presence or system events either
- Server now handles `unsubscribe` frames — previously logged as "Unknown message type", leaving the client subscribed and present until disconnect. Unsubscribing stops delivery, drops the node's Redis subscription when it was the last local subscriber, and broadcasts `presence.left` immediately (no grace period); empty/unknown/repeated unsubscribes are no-ops
- Presence is refcounted per connection, cluster-wide — closing one of a user's tabs (or their connection on another node) no longer broadcasts `presence.left`, and a second tab no longer emits a duplicate `presence.joined`. Connection IDs live in a new `orbit:presence:conns:<channel>:<userID>` sorted set updated by atomic Lua scripts that also publish the resulting event, so events are ordered with the state changes. When a leave grace timer fires it re-checks the cluster-wide count, fixing a race where a timer that had already fired announced `presence.left` for a user who had just reconnected. A further connection subscribing with `metadata` replaces the user's metadata and broadcasts `presence.updated`

### Fixed — quickstart and docs
- ✅ `docker compose up` works out of the box — `docker-compose.yml` now sets a dev-only `ORBIT_SERVER_SECRET` (the server exited on startup without it), drops the obsolete `version:` key, and waits for a Redis healthcheck before starting Orbit
- ✅ `ORBIT_ALLOWED_ORIGINS` accepts scheme-qualified entries — `coder/websocket` matches `OriginPatterns` against the `Origin` host:port only, so `http://localhost:5173` (used by the compose file, docs and CI) never matched and the WebSocket upgrade got a 403. Entries are now normalised at startup (scheme stripped for the WebSocket check, kept for CORS), so `http://localhost:5173` and `localhost:5173` both work for WebSocket and CORS
- ✅ Dev token minting — `npm run token -- <user> [channel-patterns]` (`scripts/dev-token.mjs`) prints an HS256 JWT signed with `ORBIT_JWT_SECRET` or the dev fallback; the README quickstart uses it instead of the rejected `?token=guest`, and adds a `curl` example for `POST /api/publish`
- ✅ Live cursors demo authenticates — a dev-only Vite middleware serves `GET /dev-token?user=<name>` (never part of `vite build`), and `App.jsx` fetches a token before connecting instead of passing the raw username
- ✅ Docs drift — README roadmap ticks, `/api/presence` and `/api/presence/count` response shapes, `subscribe` `ttl`/`metadata` fields, `error` envelope, `presence.rejoined`, refreshed security notice; `AGENTS.md` updated to match the current code

### Added
- Presence snapshot on subscribe — after a successful `subscribe` the server sends that connection only a `presence.state` event with `{"users":[{"user","metadata"}]}` (same shape as `GET /api/presence`, subscriber included), read after the subscription is live so no later change is missed. Metadata is now fetched with one `MGET` instead of one `GET` per user
- `presence_update` client frame — `{"type":"presence_update","channel","metadata"}` replaces the user's presence metadata (≤ 2 KB; `null`/`{}` clears) on a subscribed channel without resubscribing and broadcasts `presence.updated` `{user, metadata}` to all nodes; error frame if not subscribed or oversized; rate-limited by the per-connection inbound limiter
- JS SDK presence API (both `sdk/js/orbit.js` and `example/src/orbit.js`) — `subscribe(channel, handler, { ttl, metadata })` (options resent on reconnect), `updatePresence(channel, metadata)`, `presence(channel)` live member list (`members` Map, `count`, `synced`, `onChange(cb)`) built from `presence.state` plus buffered deltas, and `onError(cb)` for server error frames
- `tests/presence-multi-conn.test.mjs` integration test (CI now runs two server nodes against one Redis for its cross-node case) and Go unit tests in CI (`go test -race ./...`); existing test scripts honour `ORBIT_URL`

### v0.1 — Trustworthy (upcoming)
- ✅ JWT HS256 token authentication — replaces auth stub; `ORBIT_JWT_SECRET` required (min 32 chars); anonymous connections rejected
- ✅ Channel-level ACLs via JWT `channels` claim — deny by default, wildcard pattern support (`room-*`, `*`), publish denial now returns error frame
- ✅ Remove `InsecureSkipVerify` from WebSocket accept — replaced with `ORBIT_ALLOWED_ORIGINS` allowlist
- ✅ Fix JS SDK `unsubscribe()` — now sends unsubscribe frame to server when last handler is removed
- ✅ Fix `/api/presence` — all responses now return `Content-Type: application/json` (error paths were returning `text/plain`)
- ✅ Connection rate limiting and per-user connection caps — token bucket per-IP limiter (`ORBIT_RATE_LIMIT_CONNS_PER_SEC`, default 10), per-user connection cap (`ORBIT_MAX_CONNS_PER_USER`, default 10), trusted proxy support; set either to `0` to disable
- ✅ Graceful shutdown — SIGTERM/SIGINT drains fanout workers, sends WebSocket close frames to all clients, stops HTTP listener; `ORBIT_SHUTDOWN_TIMEOUT` (default `10s`)
- ✅ Slow consumer detection — per-connection outbound buffer (`ORBIT_CLIENT_BUFFER_SIZE`, default 256); consecutive-drop counter disconnects slow clients with 1008 Policy Violation after `ORBIT_SLOW_CLIENT_THRESHOLD` drops (default 50); `orbit_dropped_messages_total` incremented per drop
- ✅ Presence HTTP API requires auth — `/api/presence` and `/api/presence/count` accept the server secret or a client JWT whose subscribe ACL covers the channel (401/403 otherwise); CORS `*` replaced with an `ORBIT_ALLOWED_ORIGINS` echo plus `OPTIONS` preflight
- ✅ Fix rate-limit bypass behind a proxy — with `ORBIT_TRUSTED_PROXY=true` the limiter now keys on the rightmost `X-Forwarded-For` entry (the one the trusted proxy appended) instead of the client-controlled leftmost; unit tests for client IP extraction and the token bucket
- ✅ HTTP hardening — `/api/publish` compares the server secret in constant time, requires `Content-Type: application/json` (415), caps the body at `ORBIT_MAX_PUBLISH_BYTES` (default 65536, 413) and returns JSON on every response; `http.Server` gets `ReadHeaderTimeout` 10s and `IdleTimeout` 120s

### v0.2 — Presence Engine (in progress)
- ✅ Occupancy counts per channel — `GET /api/presence/count?channel=` returns `{channel, count}`
- ✅ Configurable presence TTLs — global `ORBIT_PRESENCE_TTL_SECONDS` (default 45, range 5–3600) plus a per-connection, per-channel `ttl` field on `subscribe` frames; out-of-range values get an `error` envelope
- ✅ Room metadata — optional `metadata` JSON (max 2 KB) on `subscribe` frames, stored with the presence entry, included in `presence.joined` and in `GET /api/presence`, which now returns `{channel, users: [{user, metadata}]}`
- ✅ REST publish API — `POST /api/publish` for backend services, authenticated with `Authorization: Bearer <ORBIT_SERVER_SECRET>`; `ORBIT_SERVER_SECRET` is now required at startup
- Presence consistency semantics (unclean disconnect, partition, Redis blip)
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
- Integration test scripts (`tests/ws-pubsub.test.js`, `tests/sdk-presence.test.mjs`)
- `ORBIT_FANOUT_WORKERS` environment variable for tuning worker pool size
