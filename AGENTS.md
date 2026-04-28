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

## Known Limitations / Future Work

- **Auth is a stub**: Replace `TokenAuthenticator` with JWT decoding or HMAC verification
- **`InsecureSkipVerify: true`** must be removed before production deployment
- **`BroadcastLocal` in `ws/gateway.go`** is unimplemented (routing is handled by `router`)
- **`CanSubscribe` / `CanPublish`** always return `true` — implement channel-level ACLs
- **Hardcoded secret** `"secret"` in server startup
- No TLS termination in binary; expected to be handled by a reverse proxy (nginx, Caddy)
- No unsubscribe message sent to server from JS SDK on `unsubscribe()` call (local cleanup only)
