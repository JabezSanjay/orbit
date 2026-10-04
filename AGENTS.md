# Orbit — Agent Reference

Self-hosted, open-source realtime infrastructure built with **Go + Redis**. A drop-in alternative to hosted platforms like Pusher.

[README.md](README.md) is the source of truth for the environment variable table, the HTTP API, and the WebSocket protocol. This file covers how the code fits together. When the two disagree with the code, trust `cmd/server/main.go`.

---

## What It Does

Orbit provides WebSocket-based Pub/Sub and Presence for connected clients. Multiple Orbit nodes scale horizontally via Redis PubSub fanout. Clients connect over a single persistent WebSocket, authenticate with an HS256 JWT, and communicate through a typed JSON envelope protocol.

---

## Tech Stack

| Layer | Technology |
|---|---|
| Server | Go 1.23 |
| Message broker | Redis 7 (via `go-redis/v9`) |
| WebSocket library | `coder/websocket` |
| Auth | HS256 JWT (`golang-jwt/jwt/v5`) |
| Metrics | Prometheus (`prometheus/client_golang`) |
| JS SDK | Vanilla JavaScript (ESM class) |
| Demo app | React + Vite |
| Containerization | Docker / Docker Compose |

---

## Repository Layout

```
orbit/
├── cmd/
│   ├── server/main.go          # Env parsing, HTTP handlers, graceful shutdown (all wiring lives here)
│   └── bench/main.go           # Redis PubSub stress-test / benchmark tool
├── internal/
│   ├── auth/auth.go            # JWTAuthenticator + channel ACL matching
│   ├── core/message.go         # Envelope type and MessageType constants
│   ├── metrics/metrics.go      # Prometheus metric definitions (promauto globals)
│   ├── presence/tracker.go     # Redis sorted-set presence tracker + metadata keys
│   ├── pubsub/
│   │   ├── pubsub.go           # Engine interface
│   │   └── redis.go            # Multiplexed Redis PubSub engine + fanout workers
│   ├── ratelimit/ratelimit.go  # Per-IP token bucket for new connections
│   ├── router/router.go        # Subscribe/publish handling, presence events, leave grace timers
│   └── ws/
│       ├── client.go           # Per-connection read/write pumps, slow-consumer detection
│       └── gateway.go          # Connection registry, per-user caps, shutdown
├── sdk/js/orbit.js             # Standalone JavaScript SDK (served at / by the server)
├── example/                    # React + Vite live-cursor demo (dev-only /dev-token middleware in vite.config.js)
├── scripts/
│   ├── dev-token.mjs           # `npm run token -- <user> [patterns]` mints a dev JWT
│   └── github-setup.sh         # Creates GitHub issues from .github/issues/*.md drafts
├── tests/                      # Node integration scripts run against a live server
├── Dockerfile                  # Multi-stage Go build → alpine runtime
├── docker-compose.yml          # Redis + Orbit with public dev-only secrets
└── go.mod                      # Go module: github.com/orbit/orbit
```

Import direction: `router` imports `ws`, `presence`, `pubsub`, and `auth`. `ws` defines its own `Router` interface to call back into the router without an import cycle.

---

## Running the Server

The server calls `log.Fatal` unless `ORBIT_JWT_SECRET` (32+ chars) and `ORBIT_SERVER_SECRET` are set. Run it from the repo root, because `/` serves `./sdk/js` relative to the working directory.

**Docker (recommended):**
```bash
docker compose up --build
```

**Local (requires Redis on :6379):**
```bash
ORBIT_JWT_SECRET=orbit-local-dev-secret-do-not-use-in-production \
ORBIT_SERVER_SECRET=orbit-server-secret-do-not-use-in-production \
go run ./cmd/server
```

**Mint a dev token:** `npm install && npm run token -- alice` (add `'room-*'` to restrict channels).

**Benchmark tool:** `go run ./cmd/bench`

**Environment variables:** see the table in [README.md](README.md#environment-variables). The server reads `PORT`, `REDIS_URL`, `ORBIT_FANOUT_WORKERS`, `ORBIT_ALLOWED_ORIGINS`, `ORBIT_JWT_SECRET`, `ORBIT_SERVER_SECRET`, `ORBIT_RATE_LIMIT_CONNS_PER_SEC`, `ORBIT_MAX_CONNS_PER_USER`, `ORBIT_TRUSTED_PROXY`, `ORBIT_SHUTDOWN_TIMEOUT`, `ORBIT_CLIENT_BUFFER_SIZE`, `ORBIT_SLOW_CLIENT_THRESHOLD`, `ORBIT_MAX_MESSAGE_BYTES`, `ORBIT_CLIENT_MSGS_PER_SEC`, `ORBIT_MAX_PUBLISH_BYTES`, `ORBIT_PRESENCE_TTL_SECONDS`, and `ORBIT_PRESENCE_LEAVE_GRACE_MS`. `ORBIT_ALLOWED_ORIGINS` entries may include a scheme or not (`http://localhost:5173` or `localhost:5173`): `parseAllowedOrigins` (`cmd/server/origins.go`) strips the scheme for `coder/websocket`'s `OriginPatterns`, which match the `Origin` header's host:port only, and keeps it for the CORS check.

---

## HTTP Endpoints

`/ws` (WebSocket upgrade), `GET /api/presence`, `GET /api/presence/count`, `POST /api/publish`, `GET /metrics`, and `/` (static `sdk/js`). Request and response shapes are in [README.md](README.md#http-api).

`/ws` runs these checks in order: shutting down → 503; per-IP rate limit → 429; JWT auth → 401; per-user connection cap → 429; WebSocket upgrade with the origin allowlist → 403.

`POST /api/publish` checks `Authorization: Bearer <ORBIT_SERVER_SECRET>` and publishes straight to the engine after rejecting reserved events (`core.IsReservedEvent`, 400). It skips the router, so there is no ACL check and no presence refresh. The presence endpoints accept `Authorization: Bearer <ORBIT_SERVER_SECRET>` or a client JWT (Bearer header or `?token=`) whose subscribe ACL covers the channel (401/403 otherwise), and echo CORS headers only for `ORBIT_ALLOWED_ORIGINS`. The HTTP handlers live in `cmd/server/api.go`.

---

## Authentication and ACLs

`auth.JWTAuthenticator` reads the token from `Authorization: Bearer` first, then `?token=`. It requires HS256, a valid signature with `ORBIT_JWT_SECRET`, an `exp` claim, and a non-empty `sub`, which becomes the userID. `Authenticate` also returns the expiry; `ws.Client` closes the connection with code `4001` when it passes.

Channel permissions come from the `channels` claim and are denied by default:

```json
{
  "sub": "user123",
  "exp": 1767225600,
  "channels": {
    "subscribe": ["room-*", "notifications-user123"],
    "publish": ["room-1"]
  }
}
```

Patterns are an exact name, a single trailing wildcard (`room-*`), or `*`. A missing claim or empty list means no access. Denials come back as an `error` envelope (`{"type":"error","payload":{"error":"..."}}`).

---

## WebSocket Protocol

The envelope and the full frame tables are in [README.md](README.md#websocket-protocol). Points that matter when changing the code:

- `ReadPump` answers `ping` with `pong` itself, then hands every frame to `DefaultRouter.HandleMessage`.
- `HandleMessage` handles `subscribe`, `unsubscribe`, `publish`, `presence_update`, and `ping`.
- `subscribe` accepts optional `ttl` (5–3600 s, else `error` and no subscribe) and `metadata` (max 2 KB, else `error` but the subscribe proceeds without metadata). After a successful subscribe the router sends that connection only a `presence.state` snapshot (`{users: [{user, metadata}]}`, subscriber included), read after the subscription is live.
- `unsubscribe` stops delivery immediately, drops the node's Redis subscription when it was the last local subscriber, and broadcasts `presence.left` right away (no grace period) unless the user still has another live connection. Unknown, empty or repeated unsubscribes are silent no-ops.
- `presence_update` (`{type, channel, metadata}`) replaces the user's metadata (≤ 2 KB; `null`/`{}`/omitted clears) on a channel this connection is subscribed to and broadcasts `presence.updated`; `error` frame if not subscribed or oversized.
- `publish` rejects reserved events (`presence.*`, `orbit.*`, see `core.IsReservedEvent`), then rebuilds the envelope server-side (dropping client `ttl`/`metadata`, stamping `sender` with the publisher's userID) and writes it, still `type: "publish"`, to the Redis channel of the same name. `handleRedisMessage` rewrites it to `message` before calling `SendJSON` for every local subscriber, including the sender.
- Presence events (`presence.joined`, `presence.rejoined`, `presence.updated`, `presence.left`) are published by the tracker's Lua scripts already typed `message` and fan out like any other message; `presence.state` goes straight to the subscribing connection.

---

## Presence Tracking

Implemented in `internal/presence/tracker.go` using Redis Sorted Sets and Lua scripts.

Presence is **tracked per connection and reported per user**, cluster-wide:

| Key | Type | Contents |
|---|---|---|
| `orbit:presence:<channel>` | ZSET | member `userID`, score = latest connection expiry (Unix seconds). Backs `Count`, `GetUsers`, the REST API. |
| `orbit:presence:conns:<channel>:<userID>` | ZSET | member connection ID, score = that connection's expiry. The cluster-wide refcount. |
| `orbit:presence:meta:<channel>:<userID>` | STRING | the user's metadata JSON |

- `Join` / `Leave` / `Finalize` / `Heartbeat` are Lua scripts, so a user's 0→1 and 1→0 transitions are atomic across nodes. `Join` and `Finalize` also `PUBLISH` the resulting presence event inside the script, so events reach the channel in the same order as the state changes (a stale `left` can never overtake a newer `joined`).
- `presence.joined`/`rejoined` fire only on the user's first live connection; `presence.left` only when `Finalize` finds no live connection on any node (and the user was still listed — at most one `left` per presence episode).
- Metadata: the first connection's subscribe `metadata` (or none) defines the user's metadata; a later connection subscribing with `metadata` overwrites it and broadcasts `presence.updated` (last write wins per user). Metadata is deleted on the 1→0 transition.
- Default TTL: **45 seconds**, configurable via `ORBIT_PRESENCE_TTL_SECONDS` (range: 5–3600s)
- Per-channel TTL override: clients may set a `ttl` (seconds) field in the `subscribe` envelope; validated 5–3600s, rejected with an `error` envelope if out of range
- TTL is refreshed on subscribe, publish, and ping (per connection; refreshes the user entry and metadata too)
- Expired users are cleaned on every `GetUsers` or `Count` call; expired connections are pruned inside the scripts
- All keys expire after `2×TTL` (only ever extended), so state from crashed nodes ages out

### Presence consistency semantics

| Case | Behaviour |
|---|---|
| **Unclean disconnect** (Wi-Fi drop, tab close) | User remains visible in presence until their sorted-set score expires (TTL). Intended design — no change. App developers should treat the TTL window as an "away" state client-side. |
| **Duplicate `subscribe` frames** from same connection | Idempotent — second subscribe for the same channel on the same connection is a no-op; no second event is emitted. |
| **Several connections, same user** (tabs, devices, nodes) | One `presence.joined` for the first, one `presence.left` after the last; the others emit nothing (except `presence.updated` if they subscribe with metadata). |
| **Reconnect within the grace window** (new connection, same userID, no other live connection) | `presence.rejoined` is emitted instead of `presence.joined`. Subscribers can use this to distinguish a clean join from a reconnect and avoid flickering the user as offline. |
| **`presence.left` grace period** | After a *disconnect* drops a user's last connection, `presence.left` is deferred by `ORBIT_PRESENCE_LEAVE_GRACE_MS` (default 2 000 ms); the user stays listed meanwhile. When the timer fires, `Finalize` re-checks the cluster-wide connection count atomically, so a re-subscribe on any node — even after the timer fired — suppresses the event. An explicit `unsubscribe` skips the grace period. |
| **Redis restart / blip** | Sorted sets are lost. The PubSub layer auto-resubscribes all channels on reconnect (exponential backoff 100 ms–5 s). Presence entries are missing until clients send their next `ping` (every 10 s from the SDK). No `presence.left` events are sent for users whose entries were lost — this is acceptable and documented here. |
| **Cross-node `presence.left` on node shutdown** | On graceful shutdown a node closes all its WebSocket connections. Clients reconnect to another node. The old node's disconnect handlers start grace-period timers (`ORBIT_PRESENCE_LEAVE_GRACE_MS`). If the client reconnects to any node within the grace window, its new connection entry makes the old node's `Finalize` a no-op (and the new node emits `presence.rejoined`). If not, `presence.left` fires normally. |

---

## Architecture: Distributed Fanout

```
Client A                     Client B (different node)
    |                                |
[Orbit Node 1]              [Orbit Node 2]
    |                                |
    +--------→  Redis PubSub ←-------+
                    |
         Broadcasts to all nodes
              that subscribed
```

- Each Orbit node maintains a single **multiplexed Redis PubSub connection**
- When a client subscribes locally, the node subscribes to that Redis channel (once per channel per node) and unsubscribes when its last local client disconnects
- Incoming Redis messages are dispatched via a **worker pool** (`ORBIT_FANOUT_WORKERS`, default 100) with FNV-hashed routing for per-channel ordering
- Worker queues are bounded at **1024 messages**; overflow is dropped and counted in `orbit_dropped_messages_total`
- `SendJSON` never blocks: a full per-client buffer (`ORBIT_CLIENT_BUFFER_SIZE`) drops the message, and `ORBIT_SLOW_CLIENT_THRESHOLD` consecutive drops close the connection with 1008
- On Redis failure: exponential backoff (100ms–5s) with jitter, then auto-resubscribes all channels
- Graceful shutdown (SIGTERM/SIGINT, bounded by `ORBIT_SHUTDOWN_TIMEOUT`): refuse new connections → drain fanout workers → send 1001 close frames → stop the HTTP server

---

## Redis Architecture Decision

Orbit uses three Redis primitives with explicitly unequal roles:

| Primitive | Role | Status |
|---|---|---|
| **PubSub** | Realtime fanout transport — hot path, always on | Core |
| **Sorted Sets** | Presence state (TTL-based, per channel) | Core |
| **Streams** | Optional durability layer for message history + replay | Opt-in (v0.5) |

**Redis Streams is not an architectural dependency.** Live delivery must work entirely without it. Replay is enabled only when explicitly configured. Orbit should never require Streams to function.

**Why keep PubSub instead of using Streams alone?**
PubSub is lower latency and simpler for hot-path fanout. Streams adds ordering and replay but introduces write overhead on every publish. For Orbit's primary use case (live presence, collaborative tools), PubSub is the right transport. Streams augments it; it does not replace it.

**Before v0.5 locks in Streams, pressure-test these four scenarios:**
1. **Fanout latency under Streams write load** — PubSub may degrade when `XADD` is busy on the same Redis instance
2. **Redis memory growth** — Streams retention can surprise; validate with realistic channel counts and retention windows
3. **Replay storm after reconnect** — 1,000 clients reconnecting and calling `XREAD` simultaneously is a different workload than steady state
4. **Presence correctness during Redis restart** — validate the Sorted Sets + PubSub + Streams recovery path together, not in isolation

If a single Redis instance cannot handle all three primitives at target load, the correct response is configurable separation (separate Redis URLs per concern), not switching to a different streaming system.

---

## Prometheus Metrics

All metrics are registered via `promauto` in `internal/metrics/metrics.go`.

| Metric | Type | Description |
|---|---|---|
| `orbit_publish_latency_seconds` | Histogram | Time to publish a message to Redis |
| `orbit_fanout_latency_seconds` | Histogram | Time from Redis receive to client write buffer |
| `orbit_active_subscriptions` | Gauge | Active Redis channel subscriptions on this node |
| `orbit_reconnects_total` | Counter | Redis reconnection events |
| `orbit_dropped_messages_total` | Counter | Messages dropped by saturated fanout queues or full client buffers | | `orbit_inbound_rate_limited_total` | Counter | Inbound client frames dropped by the per-connection rate limit (`ORBIT_CLIENT_MSGS_PER_SEC`) |
| `orbit_inbound_rate_limited_total` | Counter | Inbound client frames dropped by the per-connection rate limit (`ORBIT_CLIENT_MSGS_PER_SEC`) |
| `orbit_active_connections` | Gauge | Active WebSocket connections on this node |

---

## JavaScript SDK (`sdk/js/orbit.js` / `example/src/orbit.js`)

```js
import { Orbit } from './sdk/js/orbit.js';

const orbit = new Orbit(`ws://localhost:8080/ws?token=${jwt}`); // jwt minted by your backend

orbit.onConnected(() => { /* fires on connect and each reconnect */ });

orbit.subscribe("my-channel", (msg) => {
  // msg.type === "message"
  // msg.channel, msg.event, msg.payload, msg.sender (client publishes)
}, { ttl: 30, metadata: { name: "Alice" } }); // options are optional

// Live member list, maintained from presence.state + joined/rejoined/updated/left.
const room = orbit.presence("my-channel");
room.onChange((members) => console.log([...members.keys()])); // Map userID -> metadata

orbit.updatePresence("my-channel", { status: "away" }); // -> presence.updated

orbit.onError((message, frame) => console.warn(message)); // server error frames

orbit.publish("my-channel", { text: "hello" }); // only sent while the socket is open

orbit.unsubscribe("my-channel", handler);

orbit.disconnect(); // example/src/orbit.js only: intentional close, no auto-reconnect
```

**SDK behavior:**
- Auto-reconnects every 3 seconds on unexpected disconnect, reusing the same URL (and token)
- Sends `ping` every 10 seconds
- Automatically re-sends all `subscribe` frames after reconnect, with their `ttl`/`metadata` (the latest `updatePresence` metadata wins)
- Sends `unsubscribe` when the last handler for a channel is removed
- `orbit.presence(channel)` buffers presence deltas until the subscribe's `presence.state` snapshot arrives, then applies them on top per user (last event wins); `synced` is false until then
- Error frames go to `onError` callbacks (logged with `console.warn` when none are registered)
- `sdk/js/orbit.js` and `example/src/orbit.js` are hand-copied duplicates; the example copy adds an `intentionalClose` flag and `disconnect()`. Keep both in sync.

---

## Example Demo App (`example/`)

React + Vite live cursor demo on the `live-canvas` channel. See [example/README.md](example/README.md).

```bash
docker compose up --build   # sets ORBIT_ALLOWED_ORIGINS=localhost:5173
cd example && npm install && npm run dev
```

- The app fetches a JWT from `GET /dev-token?user=<name>`, a dev-only Vite middleware (`apply: 'serve'`) in `vite.config.js` that signs with `ORBIT_JWT_SECRET` or the dev fallback
- `cursor.move` events with normalized `(nx, ny)` coordinates (0–1 relative to viewport), throttled to ~25 FPS
- Client-side cursor smoothing via linear interpolation (LERP) with `requestAnimationFrame`
- Round-trip latency measurement via `latency.ping` events
- Peers removed on `presence.left`

---

## Integration Tests

Go unit tests live next to the code (`internal/auth`, `internal/router`, `internal/ws`, `internal/ratelimit`); run `go test -race ./...`. CI runs `go vet`, builds both binaries, then these Node scripts against a live server (`npm install` at the repo root first):

| File | What it tests |
|---|---|
| `tests/ws-pubsub.test.js` | Raw `ws` WebSocket: subscribe to `room-1`, publish, print what comes back (always exits 0) |
| `tests/sdk-presence.test.mjs` | Orbit SDK (`example/src/orbit.js`): connect, subscribe to `global-hub`, exit 1 if `presence.joined` never arrives |
| `tests/rest-publish.test.mjs` | REST publish API: POST to `/api/publish`, verify WS subscriber receives it; 400 (incl. reserved events)/401/405/413/415 cases |
| `tests/presence-api.test.mjs` | Presence HTTP API auth: server secret, client JWT ACL (401/403), CORS allowlist echo and preflight |
| `tests/presence-multi-conn.test.mjs` | Per-connection presence (two tabs: one joined, closing one emits no left, closing both emits one left after grace), `presence.state` snapshot, `presence_update`, `unsubscribe`, SDK `presence()` helper; cross-node case when `ORBIT_URL_2` points at a second node. Set `ORBIT_PRESENCE_LEAVE_GRACE_MS` to the server's value. |

The scripts sign tokens with `ORBIT_JWT_SECRET` and use `ORBIT_SERVER_SECRET` (for `/api/publish` and the authenticated presence API), falling back to the same dev values as `docker-compose.yml`. All of them honour `ORBIT_URL` (default `http://localhost:8080`) and exit non-zero on failure (`ws-pubsub.test.js` only prints). CI starts two server nodes (`:8080`, `:8081`) on one Redis. The presence tracker's Lua tests run only when `ORBIT_TEST_REDIS_URL` is set.

Run with Node.js after starting the server:
```bash
node tests/ws-pubsub.test.js
node tests/sdk-presence.test.mjs
node tests/rest-publish.test.mjs
node tests/presence-api.test.mjs
node tests/presence-multi-conn.test.mjs
```

---

## Benchmark Tool (`cmd/bench/main.go`)

Stress-tests the Redis PubSub engine directly (no WebSocket layer):

- Subscribes to **10,000 channels** concurrently
- Publishes randomly at ~1 message/µs
- Logs throughput (messages/sec) every 2 seconds
- Exposes pprof on `:6060` and Prometheus metrics on `:6060/metrics`

---

## Known Limitations

- **`BroadcastLocal` in `ws/gateway.go`** is an empty extension point (routing is handled by `router`)
- **Presence metadata is per user**, so a user's connections overwrite each other's metadata (last write wins)
- No TLS termination in the binary; expected to be handled by a reverse proxy (nginx, Caddy)

---

## Roadmap

Orbit's thesis: **self-hosted realtime infrastructure with strong presence primitives**. All agents and contributors **must align work to a version** before implementing features.

| Version | Focus |
|---|---|
| v0.1 | Security + survivability |
| v0.2 | Presence engine |
| v0.3 | Adoption + developer onboarding |
| v0.5 | Durable messaging + observability |
| v1.0 | Platform ecosystem |

### v0.1 — Trustworthy *(security + survivability)*
> Blockers before any public deployment.

- [x] Replace auth stub with real JWT / HMAC token validation
- [x] Remove `InsecureSkipVerify` from WebSocket accept
- [x] Implement `CanSubscribe` / `CanPublish` channel-level ACLs
- [x] Fix JS SDK `unsubscribe()` to send an unsubscribe frame to the server
- [x] Connection rate limiting and per-user connection caps
- [x] Graceful shutdown with in-flight message draining
- [x] Slow consumer detection — per-connection outbound buffer limits and drop policy

### v0.2 — Presence Engine
> Focus Orbit on the presence and multiplayer primitive space.

- [x] Occupancy counts per channel (live member count API)
- [x] Configurable presence TTLs per channel
- [x] Room metadata — attach arbitrary state to a presence entry
- [ ] Presence consistency semantics — document and harden behavior on unclean disconnect, network partition, duplicate joins, Redis blip
- [x] REST publish API — publish to a channel over HTTP (unlocks backend integrations: cron jobs, APIs, workers)
- [ ] Load tests: 10k, 50k, 100k connections with published benchmarks

### v0.3 — Adoption *(developer onboarding)*
> Make it trivial to try, evaluate, and migrate to Orbit.

- [ ] "Build live cursors in 5 minutes" quickstart
- [ ] Next.js + React integration examples
- [ ] Go backend integration example
- [ ] "Migrate from Pusher" guide
- [ ] `BENCHMARK.md` — real numbers on a $5 VPS

### v0.5 — Observable *(durable messaging + observability)*
> Safe to run in real environments, with full visibility.

- [ ] Message history + replay via Redis Streams (`XADD` / `XREAD`)
- [ ] TLS termination guide + example nginx / Caddy configs
- [ ] Structured JSON logging with configurable log levels
- [ ] Official Grafana dashboard (pre-built, importable)
- [ ] Official Docker Hub image + versioned releases

### v1.0 — Platform *(ecosystem + resilience)*
> Complete platform with failure-tested infrastructure.

- [ ] Chaos testing — Redis kill, node restart, network drop, channel flood
- [ ] Go client SDK
- [ ] Python SDK
- [ ] Webhooks — fire HTTP callbacks on publish / presence events
- [ ] Helm chart for Kubernetes deployments
- [ ] Protocol compatibility guarantees — SemVer policy for wire protocol and SDK breaking changes

> **Scope guard:** Orbit is meant to be understandable in an afternoon. Features that require a config file longer than 50 lines belong in a different project. When in doubt, do less.
