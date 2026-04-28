# Orbit — Agent Reference

Self-hosted, open-source realtime infrastructure built with **Go + Redis**. A drop-in alternative to hosted platforms like Pusher.

---

## What It Does

Orbit provides WebSocket-based Pub/Sub and Presence for connected clients. Multiple Orbit nodes scale horizontally via Redis PubSub fanout. Clients connect over a single persistent WebSocket and communicate through a typed JSON envelope protocol.

---

## Tech Stack

| Layer | Technology |
|---|---|
| Server | Go 1.23 |
| Message broker | Redis 7 (via `go-redis/v9`) |
| WebSocket library | `coder/websocket` |
| Metrics | Prometheus (`prometheus/client_golang`) |
| JS SDK | Vanilla JavaScript (ESM class) |
| Demo app | React + Vite |
| Containerization | Docker / Docker Compose |

---

## Repository Layout

```
orbit/
├── cmd/
│   ├── server/main.go          # Main HTTP + WebSocket server binary
│   └── bench/main.go           # Redis PubSub stress-test / benchmark tool
├── internal/
│   ├── auth/auth.go            # Authenticator interface + token stub
│   ├── core/message.go         # Core Envelope type and MessageType constants
│   ├── metrics/metrics.go      # Prometheus metric definitions
│   ├── presence/tracker.go     # Redis sorted-set presence tracker
│   ├── pubsub/
│   │   ├── pubsub.go           # Engine interface definition
│   │   └── redis.go            # Redis PubSub engine implementation
│   ├── router/router.go        # Message routing + subscription management
│   └── ws/
│       ├── client.go           # Per-connection WebSocket client (read/write pumps)
│       └── gateway.go          # Local connection registry
├── sdk/js/orbit.js             # Standalone JavaScript SDK
├── example/                    # React + Vite live-cursor demo app
│   └── src/
│       ├── App.jsx             # Main demo component (cursor tracking)
│       ├── orbit.js            # Extended JS SDK (with disconnect support)
│       └── components/Cursor.jsx  # Animated LERP cursor component
├── tests/
│   ├── ws-pubsub.test.js       # Node.js raw WebSocket integration test
│   └── sdk-presence.test.js    # Node.js Orbit SDK integration test
├── Dockerfile                  # Multi-stage Go build → alpine runtime
├── docker-compose.yml          # Redis + Orbit service definitions
└── go.mod                      # Go module: github.com/orbit/orbit
```

---

## Go Module

```
module github.com/orbit/orbit
go 1.23
```

Key external dependencies: `coder/websocket`, `google/uuid`, `prometheus/client_golang`, `redis/go-redis/v9`.

---

## Running the Server

**Docker (recommended):**
```bash
docker-compose up --build
```

**Local (requires Redis on :6379):**
```bash
go run ./cmd/server
```

**Benchmark tool:**
```bash
go run ./cmd/bench
```

---

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP server listen port |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection URL |
| `ORBIT_FANOUT_WORKERS` | `100` | Number of worker goroutines for Redis message dispatch |

---

## HTTP Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/ws?token=<token>` | WebSocket upgrade. `token` query param is the auth credential. |
| `GET` | `/metrics` | Prometheus metrics scrape endpoint. |
| `GET` | `/api/presence?channel=<channel>` | Returns JSON array of active user IDs in a channel. |

---

## WebSocket Protocol

All messages use a single JSON envelope structure:

```ts
{
  type: "subscribe" | "unsubscribe" | "publish" | "message" | "ping" | "pong",
  channel?: string,
  event?: string,
  payload?: any
}
```

### Client → Server messages

| `type` | Purpose |
|---|---|
| `subscribe` | Join a channel. Server begins forwarding messages from that channel. |
| `unsubscribe` | Leave a channel. |
| `publish` | Broadcast a message to a channel. `event` and `payload` are forwarded to all subscribers. |
| `ping` | Heartbeat. Server responds with `pong` and refreshes presence TTL. |

### Server → Client messages

| `type` | Purpose |
|---|---|
| `message` | Incoming message from a subscribed channel. |
| `pong` | Response to a `ping`. |
| `error` | Access control rejection or bad request. |

### System presence events (delivered as `type: "message"`)

| `event` | Triggered when |
|---|---|
| `presence.joined` | A user subscribes to a channel. `payload.user` = userID. |
| `presence.left` | A user disconnects or unsubscribes. `payload.user` = userID. |

---

## Authentication

**Current state: MVP stub.** No real JWT validation.

- The `token` query parameter is mapped directly: `userID = "user_" + token`
- Empty token → `userID = "anonymous"`
- Every user can subscribe and publish to every channel
- The `Authenticator` interface (`internal/auth/auth.go`) is designed to be replaced with real JWT or HMAC-based auth

The hardcoded secret `"secret"` in `cmd/server/main.go` is a placeholder.

**`InsecureSkipVerify: true`** is set on WebSocket accept for cross-origin dev; remove for production.

---

## Presence Tracking

Implemented in `internal/presence/tracker.go` using Redis Sorted Sets.

- Redis key: `orbit:presence:<channel>`
- Each member is a `userID`; score is the **Unix expiry timestamp**
- TTL: **45 seconds** (refreshed on subscribe, publish, and ping)
- Expired members are cleaned on every `GetUsers` or `Count` call
- `GetUsers` returns currently active users (after cleaning)
- A TTL of `2×45s = 90s` is set on the entire key to prevent abandoned keys

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
- When a client subscribes locally, the node subscribes to that Redis channel (once per channel per node)
- Incoming Redis messages are dispatched via a **worker pool** (default 100 workers) with FNV-hashed routing for per-channel ordering
- Worker queues are bounded at **1024 messages**; overflow is dropped and counted in `orbit_dropped_messages_total`
- On Redis network partition: exponential backoff (100ms–5s) with jitter, then auto-resubscribes all channels

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
| `orbit_dropped_messages_total` | Counter | Messages dropped due to saturated worker queues |
| `orbit_active_connections` | Gauge | Active WebSocket connections on this node |

---

## JavaScript SDK (`sdk/js/orbit.js` / `example/src/orbit.js`)

```js
import { Orbit } from './sdk/js/orbit.js';

const orbit = new Orbit("ws://localhost:8080/ws?token=myuser");

orbit.onConnected(() => { /* fires on connect and each reconnect */ });

orbit.subscribe("my-channel", (msg) => {
  // msg.type === "message"
  // msg.channel, msg.event, msg.payload
});

orbit.publish("my-channel", { text: "hello" });

orbit.unsubscribe("my-channel", handler);

orbit.disconnect(); // intentional close, no auto-reconnect
```

**SDK behavior:**
- Auto-reconnects every 3 seconds on unexpected disconnect
- Sends `ping` every 10 seconds (server write timeout is 15 seconds, ping period is 15 seconds)
- Automatically re-sends all `subscribe` frames after reconnect
- `example/src/orbit.js` extends the SDK with an `intentionalClose` flag and `disconnect()` method

---

## Example Demo App (`example/`)

React + Vite live cursor demo on the `live-canvas` channel.

**To run:**
```bash
cd example
npm install
npm run dev
```

**What it demonstrates:**
- Real-time cursor position sharing across browser tabs/devices
- `cursor.move` events with normalized `(nx, ny)` coordinates (0–1 relative to viewport)
- Client-side cursor smoothing via **linear interpolation (LERP)** at 30fps via `requestAnimationFrame`
- Publish throttled to **~25 FPS** (40ms interval)
- Round-trip latency measurement via `latency.ping` events
- Presence via `presence.joined` / `presence.left` events
- Deterministic color assignment per user (FNV-style hash)

---

## Integration Tests

| File | What it tests |
|---|---|
| `tests/ws-pubsub.test.js` | Raw `ws` WebSocket: subscribe to `room-1`, publish, verify message received back |
| `tests/sdk-presence.test.js` | Orbit SDK: connect, subscribe to `global-hub`, verify `presence.joined` fires |

Run with Node.js after starting the server:
```bash
node tests/ws-pubsub.test.js
node tests/sdk-presence.test.js
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

- **Auth is a stub**: Replace `TokenAuthenticator` with JWT decoding or HMAC verification
- **`InsecureSkipVerify: true`** must be removed before production deployment
- **`BroadcastLocal` in `ws/gateway.go`** is unimplemented (routing is handled by `router`)
- **`CanSubscribe` / `CanPublish`** always return `true` — implement channel-level ACLs
- **Hardcoded secret** `"secret"` in server startup
- No TLS termination in binary; expected to be handled by a reverse proxy (nginx, Caddy)
- No unsubscribe message sent to server from JS SDK on `unsubscribe()` call (local cleanup only)

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

- [ ] Replace auth stub with real JWT / HMAC token validation
- [ ] Remove `InsecureSkipVerify` from WebSocket accept
- [ ] Implement `CanSubscribe` / `CanPublish` channel-level ACLs
- [ ] Fix JS SDK `unsubscribe()` to send an unsubscribe frame to the server
- [ ] Connection rate limiting and per-user connection caps
- [ ] Graceful shutdown with in-flight message draining
- [ ] Slow consumer detection — per-connection outbound buffer limits and drop policy

### v0.2 — Presence Engine
> Focus Orbit on the presence and multiplayer primitive space.

- [ ] Occupancy counts per channel (live member count API)
- [ ] Configurable presence TTLs per channel
- [ ] Room metadata — attach arbitrary state to a presence entry
- [ ] Presence consistency semantics — document and harden behavior on unclean disconnect, network partition, duplicate joins, Redis blip
- [ ] REST publish API — publish to a channel over HTTP (unlocks backend integrations: cron jobs, APIs, workers)
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
