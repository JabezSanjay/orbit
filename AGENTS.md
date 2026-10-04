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

**Environment variables:** see the table in [README.md](README.md#environment-variables). The server reads `PORT`, `REDIS_URL`, `ORBIT_FANOUT_WORKERS`, `ORBIT_ALLOWED_ORIGINS`, `ORBIT_JWT_SECRET`, `ORBIT_SERVER_SECRET`, `ORBIT_RATE_LIMIT_CONNS_PER_SEC`, `ORBIT_MAX_CONNS_PER_USER`, `ORBIT_TRUSTED_PROXY`, `ORBIT_SHUTDOWN_TIMEOUT`, `ORBIT_CLIENT_BUFFER_SIZE`, `ORBIT_SLOW_CLIENT_THRESHOLD`, `ORBIT_MAX_MESSAGE_BYTES`, `ORBIT_CLIENT_MSGS_PER_SEC`, `ORBIT_MAX_PUBLISH_BYTES`, `ORBIT_PRESENCE_TTL_SECONDS`, and `ORBIT_PRESENCE_LEAVE_GRACE_MS`. `ORBIT_ALLOWED_ORIGINS` entries are host patterns without a scheme (`localhost:5173`), because `coder/websocket` matches them against the `Origin` header's host.

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
- `HandleMessage` handles `subscribe`, `publish`, and `ping`. **`unsubscribe` has no case yet**: the server logs "Unknown message type" and the client stays subscribed and present until it disconnects.
- `subscribe` accepts optional `ttl` (5–3600 s, else `error` and no subscribe) and `metadata` (max 2 KB, else `error` but the subscribe proceeds without metadata).
- `publish` rejects reserved events (`presence.*`, `orbit.*`, see `core.IsReservedEvent`), then rebuilds the envelope server-side (dropping client `ttl`/`metadata`, stamping `sender` with the publisher's userID) and writes it, still `type: "publish"`, to the Redis channel of the same name. `handleRedisMessage` rewrites it to `message` before calling `SendJSON` for every local subscriber, including the sender.
- Presence events (`presence.joined`, `presence.rejoined`, `presence.left`) are published already typed `message` and fan out like any other message.

---

## Presence Tracking

Implemented in `internal/presence/tracker.go` using Redis Sorted Sets.

- `orbit:presence:<channel>` — sorted set, member = userID, score = Unix expiry time
- `orbit:presence:meta:<channel>:<userID>` — the subscribe frame's `metadata` JSON
- Both expire after 2×TTL. Expired members are removed lazily on `Count` / `GetUsers`.
- Default TTL is `ORBIT_PRESENCE_TTL_SECONDS` (45); a subscribe frame's `ttl` overrides it for that connection and channel.
- Subscribe, publish, and ping all refresh presence.
- Presence is keyed by userID, not by connection.

### Presence consistency semantics

| Case | Behaviour |
|---|---|
| **Unclean disconnect** (Wi-Fi drop, tab close) | User remains visible in presence until their sorted-set score expires (TTL). Intended design — no change. App developers should treat the TTL window as an "away" state client-side. |
| **Duplicate `subscribe` frames** from same connection | Idempotent — second subscribe for the same channel on the same connection is a no-op; no second event is emitted. |
| **Reconnect within TTL window** (new connection, same userID) | `presence.rejoined` is emitted instead of `presence.joined`. Subscribers can use this to distinguish a clean join from a reconnect and avoid flickering the user as offline. |
| **`presence.left` grace period** | `presence.left` is deferred by `ORBIT_PRESENCE_LEAVE_GRACE_MS` (default 2 000 ms). If the user re-subscribes to the channel within the window, the event is suppressed entirely. The presence entry is also kept in Redis during the window. |
| **Redis restart / blip** | Sorted sets are lost. The PubSub layer auto-resubscribes all channels on reconnect (exponential backoff 100 ms–5 s). Presence entries are missing until clients send their next `ping` (every 10 s from the SDK). No `presence.left` events are sent for users whose entries were lost — this is acceptable and documented here. |
| **Cross-node `presence.left` on node shutdown** | On graceful shutdown a node closes all its WebSocket connections. Clients reconnect to another node. The old node's disconnect handlers start grace-period timers (`ORBIT_PRESENCE_LEAVE_GRACE_MS`). If the client reconnects to any node within the grace window, the new node cancels the timer (via Redis sorted-set `IsPresent` check + `cancelLeaveTimer`). If not, `presence.left` fires normally. |

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
| `orbit_dropped_messages_total` | Counter | Messages dropped by saturated fanout queues or full client buffers |
| `orbit_active_connections` | Gauge | Active WebSocket connections on this node |

---

## JavaScript SDK (`sdk/js/orbit.js` / `example/src/orbit.js`)

```js
import { Orbit } from './sdk/js/orbit.js';

const orbit = new Orbit(`ws://localhost:8080/ws?token=${jwt}`); // jwt minted by your backend

orbit.onConnected(() => { /* fires on connect and each reconnect */ });

orbit.subscribe("my-channel", (msg) => {
  // msg.type === "message"
  // msg.channel, msg.event, msg.payload
});

orbit.publish("my-channel", { text: "hello" }); // only sent while the socket is open

orbit.unsubscribe("my-channel", handler);

orbit.disconnect(); // example/src/orbit.js only: intentional close, no auto-reconnect
```

**SDK behavior:**
- Auto-reconnects every 3 seconds on unexpected disconnect, reusing the same URL (and token)
- Sends `ping` every 10 seconds
- Re-sends all `subscribe` frames after reconnect
- Sends `unsubscribe` when the last handler for a channel is removed (the server ignores it for now)
- Does not surface `error` frames to callers
- `sdk/js/orbit.js` and `example/src/orbit.js` are hand-copied duplicates; the example copy adds `disconnect()`. Keep both in sync.

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

The scripts sign tokens with `ORBIT_JWT_SECRET` and use `ORBIT_SERVER_SECRET`, falling back to the same dev values as `docker-compose.yml`. All of them honour `ORBIT_URL` (default `http://localhost:8080`).

---

## Benchmark Tool (`cmd/bench/main.go`)

Stress-tests the Redis PubSub engine directly (no WebSocket layer):

- Subscribes to **10,000 channels** concurrently
- Publishes randomly at ~1 message/µs
- Logs throughput (messages/sec) every 2 seconds
- Exposes pprof on `:6060` and Prometheus metrics on `:6060/metrics`

---

## Known Limitations

- **`unsubscribe` frames are ignored** by the server (see above)
- **Presence is per user, not per connection**: one of a user's connections closing can emit `presence.left` while another is still subscribed
- **`BroadcastLocal` in `ws/gateway.go`** is an empty extension point (routing is handled by `router`)
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
