<div align="center">
  <h1>Orbit</h1>
  <p><strong>Self-hosted Pusher alternative in Go.</strong></p>
  <p>Realtime messaging, zero friction.</p>

  <a href="https://golang.org/"><img src="https://img.shields.io/badge/Made%20with-Go-1f425f.svg" alt="Made with Go"></a>
  <a href="https://redis.io/"><img src="https://img.shields.io/badge/Powered%20by-Redis-dc382d.svg" alt="Powered by Redis"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License"></a>
  <a href="https://github.com/JabezSanjay/orbit/actions/workflows/ci.yml"><img src="https://github.com/JabezSanjay/orbit/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/Go-1.23-00ADD8?logo=go" alt="Go 1.23">
  <img src="https://img.shields.io/badge/Redis-7-dc382d?logo=redis&logoColor=white" alt="Redis 7">
</div>

<br />

Self-hosted realtime infrastructure for WebSockets, Pub/Sub and Presence.

Built with Go + Redis. An open source alternative to hosted realtime platforms.

`docker compose up`. Connect clients. Publish events.

---

## Why Orbit

Add realtime features without building realtime infrastructure.

Orbit handles:
- Presence tracking
- Channel Pub/Sub
- Distributed WebSocket fanout
- Backpressure protection
- Prometheus metrics
- Redis-backed scaling

---

## Why not Pusher?

Use Orbit when you want:
- Self-hosting
- No per-connection pricing
- Infrastructure control
- Open source extensibility

---

## Start in 30 seconds

**1. Start Orbit and Redis:**
```bash
docker compose up --build
```

`docker-compose.yml` ships public **dev-only** secrets (`ORBIT_JWT_SECRET`, `ORBIT_SERVER_SECRET`). Replace both before running Orbit anywhere else.

**2. Mint a dev token.** Orbit only accepts signed JWTs, so a bare username won't connect. From the repo root:
```bash
npm install
npm run token -- alice            # subscribe + publish on every channel
npm run token -- alice 'room-*'   # or restrict to a comma-separated list of patterns
```

This prints an HS256 JWT valid for 1 hour, signed with `ORBIT_JWT_SECRET` (or the dev secret if unset). Use `npm run -s token -- alice` to get only the token, e.g. `TOKEN=$(npm run -s token -- alice)`. In production, your own backend mints these tokens; see [Authentication](#authentication).

**3. Connect, subscribe, publish:**
```javascript
import { Orbit } from './sdk/js/orbit.js';

const TOKEN = '<paste the token from step 2>';
const orbit = new Orbit(`ws://localhost:8080/ws?token=${TOKEN}`);

orbit.subscribe("room-1", (message) => {
    console.log("Received:", message.event, message.payload);
});

orbit.onConnected(() => {
    orbit.publish("room-1", { text: "Hello!" });
});
```

**4. Publish from a backend over HTTP** (uses the dev `ORBIT_SERVER_SECRET` from `docker-compose.yml`):
```bash
curl -X POST http://localhost:8080/api/publish \
  -H "Authorization: Bearer orbit-server-secret-do-not-use-in-production" \
  -H "Content-Type: application/json" \
  -d '{"channel":"room-1","event":"greeting","payload":{"text":"Hello from curl"}}'
# {"ok":true}
```

### Authentication

Clients pass a JWT as `?token=<jwt>` or `Authorization: Bearer <jwt>` on `/ws`. It must be HS256-signed with `ORBIT_JWT_SECRET` and contain `sub` (the user ID) and `exp`. Channel access comes from the `channels` claim and is denied by default:

```json
{
  "sub": "alice",
  "exp": 1767225600,
  "channels": {
    "subscribe": ["room-*", "notifications-alice"],
    "publish": ["room-1"]
  }
}
```

Each pattern is an exact channel name, a trailing wildcard (`room-*`), or `*` for every channel.

---

## Use Cases

- Collaborative apps
- Live dashboards
- Notifications
- Multiplayer state sync
- Presence systems

---

## Architecture

```text
       Clients
          ↓
     Orbit Nodes
          ↓
     Redis PubSub
          ↓
  Other Orbit Nodes
          ↓
 Connected Subscribers
```

Each Orbit node holds a single multiplexed Redis PubSub connection. Messages are dispatched to local subscribers via a hashed worker pool (default: 100 workers) ensuring per-channel ordering with bounded backpressure.

---

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP server listen port |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection URL |
| `ORBIT_FANOUT_WORKERS` | `100` | Worker goroutines for Redis message dispatch |
| `ORBIT_ALLOWED_ORIGINS` | _(same-origin only)_ | Comma-separated list of allowed origins. The WebSocket upgrade matches the `Origin` header's host and port only, with no scheme (e.g. `localhost:5173,myapp.com,*.myapp.com`); a value like `http://localhost:5173` never matches there. Also the CORS allowlist for the presence HTTP endpoints: only these origins are echoed in `Access-Control-Allow-Origin`. |
| `ORBIT_JWT_SECRET` | _(required)_ | HS256 signing secret — minimum 32 characters. Server refuses to start if unset or too short. |
| `ORBIT_RATE_LIMIT_CONNS_PER_SEC` | `10` | Max new WebSocket connections per second per IP. Set to `0` to disable (e.g. for load testing). |
| `ORBIT_MAX_CONNS_PER_USER` | `10` | Max simultaneous WebSocket connections per userID. Set to `0` to disable. |
| `ORBIT_TRUSTED_PROXY` | `false` | Set to `true` to trust `X-Real-IP` / `X-Forwarded-For` for IP rate limiting. Use only behind exactly one trusted reverse proxy: `X-Real-IP` is used if present (the proxy **must overwrite** it, e.g. nginx `proxy_set_header X-Real-IP $remote_addr`), otherwise the **rightmost** `X-Forwarded-For` entry (the one the proxy appended). |
| `ORBIT_SHUTDOWN_TIMEOUT` | `10s` | Maximum time to drain in-flight messages and close connections on SIGTERM/SIGINT (e.g. `30s`, `1m`) |
| `ORBIT_CLIENT_BUFFER_SIZE` | `256` | Per-connection outbound message buffer size. Increase for bursty workloads; messages beyond the buffer are dropped. |
| `ORBIT_SLOW_CLIENT_THRESHOLD` | `50` | Consecutive drops before a slow client is disconnected with close code 1008. Set to `0` to disable forced disconnect (drops only). |
| `ORBIT_MAX_MESSAGE_BYTES` | `65536` | Maximum size in bytes of a single inbound WebSocket frame. Larger frames close the connection with code 1009. |
| `ORBIT_CLIENT_MSGS_PER_SEC` | `100` | Per-connection inbound frame rate (burst 2×; pings count). Excess frames are dropped with an `error` frame (at most one per second); 3 consecutive seconds over the limit closes the connection with 1008. Set to `0` to disable. |
| `ORBIT_PRESENCE_TTL_SECONDS` | `45` | How long (in seconds) a user is considered online after their last activity. Accepted range: 5–3600. Per-channel overrides can be set via the `ttl` field in a `subscribe` frame. |
| `ORBIT_PRESENCE_LEAVE_GRACE_MS` | `2000` | Milliseconds to wait before broadcasting `presence.left` after a disconnect. Reconnects within this window suppress the event. Set to `0` to disable (immediate). |
| `ORBIT_SERVER_SECRET` | _(required)_ | Shared secret for backend callers: required by `POST /api/publish` and accepted by the presence endpoints. Server refuses to start if unset. Must never be exposed to WebSocket clients. |
| `ORBIT_MAX_PUBLISH_BYTES` | `65536` | Maximum request body size (bytes) accepted by `POST /api/publish`. Larger bodies are rejected with `413`. |

---

## HTTP API

| Method | Path | Description |
|---|---|---|
| `GET` | `/ws?token=<jwt>` | WebSocket upgrade endpoint. The JWT can also be sent as `Authorization: Bearer <jwt>`; see [Authentication](#authentication). |
| `GET` | `/api/presence?channel=<channel>` | Returns the channel's active users and their metadata: `{"channel": "room-1", "users": [{"user": "alice", "metadata": {}}]}`. **Requires auth:** `Authorization: Bearer <ORBIT_SERVER_SECRET>`, or a client JWT (Bearer header or `?token=`) whose `channels.subscribe` ACL covers the channel. 401 if missing/invalid, 403 if the ACL denies the channel. |
| `GET` | `/api/presence/count?channel=<channel>` | Returns the channel's occupancy count: `{"channel": "room-1", "count": 3}`. Same auth rules as `/api/presence`. |
| `POST` | `/api/publish` | Publish a message to a channel from a backend service. Requires `Authorization: Bearer <ORBIT_SERVER_SECRET>` and `Content-Type: application/json` (`415` otherwise); body limited to `ORBIT_MAX_PUBLISH_BYTES` (`413` if larger). All responses, including errors, are JSON. |
| `GET` | `/metrics` | Prometheus metrics scrape endpoint |

---

## WebSocket Protocol

All messages use a single JSON envelope:

```json
{
  "type": "subscribe | unsubscribe | publish | message | ping | pong | error",
  "channel": "room-1",
  "event": "my-event",
  "payload": {}
}
```

**Client → Server**

| `type` | Purpose |
|---|---|
| `subscribe` | Join a channel. Requires a matching `channels.subscribe` pattern in the JWT. Optional fields below. |
| `unsubscribe` | Not handled by the server yet: the frame is logged and ignored, and the connection stays subscribed and present until it disconnects. The JS SDK sends it anyway. |
| `publish` | Broadcast `event` + `payload` to all channel subscribers, including the sender. Requires a matching `channels.publish` pattern. |
| `ping` | Heartbeat — server replies with `pong` and refreshes presence TTL |

Optional `subscribe` fields:

| Field | Purpose |
|---|---|
| `ttl` | Presence TTL in seconds for this connection on this channel, 5–3600. Overrides `ORBIT_PRESENCE_TTL_SECONDS`. Out-of-range values get an `error` and the subscribe is rejected. |
| `metadata` | Any JSON value, max 2 KB, stored with the presence entry and included in `presence.joined` and `GET /api/presence`. If it is too large you get an `error`, but the subscribe still succeeds without metadata. |

```json
{ "type": "subscribe", "channel": "room-1", "ttl": 30, "metadata": { "name": "Alice" } }
```

**Server → Client**

| `type` | Purpose |
|---|---|
| `message` | Incoming broadcast from a subscribed channel. Client publishes carry a server-stamped `sender` (the publisher's user ID); REST publishes have none. |
| `pong` | Heartbeat response |
| `error` | Access control rejection or bad request |

`error` frames carry no `channel`; the reason is in `payload.error`:

```json
{ "type": "error", "payload": { "error": "not authorized to subscribe to this channel" } }
```

Current reasons: `not authorized to subscribe to this channel`, `not authorized to publish to this channel`, `ttl must be between 5 and 3600 seconds`, `metadata exceeds 2 KB limit`, `event names starting with "presence." or "orbit." are reserved`, `rate limit exceeded`.

**Built-in presence events** (delivered as `type: "message"`):

| `event` | When |
|---|---|
| `presence.joined` | A user subscribes to a channel. `payload` = `{ "user": "<userID>", "metadata": {...} }` |
| `presence.rejoined` | Same as `presence.joined`, but the user was still present (e.g. a reconnect within the TTL window) |
| `presence.left` | A user's connection closes. Sent after `ORBIT_PRESENCE_LEAVE_GRACE_MS` and skipped if they resubscribe first. `payload` = `{ "user": "<userID>" }` |

Presence is tracked per user ID, not per connection, and users who vanish without a clean close stay present until their TTL expires.

---

## Demo

The `example/` directory contains a React + Vite live cursor app — open two browser tabs and see cursors move in real time.

```bash
docker compose up --build   # already allows the localhost:5173 origin
cd example
npm install
npm run dev                 # http://localhost:5173
```

The page is served from `:5173`, which is cross-origin to Orbit on `:8080`, so the server must list it in `ORBIT_ALLOWED_ORIGINS` (unset means same-origin only). `docker-compose.yml` sets this already. If you run the server with `go run ./cmd/server`, add `ORBIT_ALLOWED_ORIGINS=localhost:5173`. Entries are host patterns with no scheme; `http://localhost:5173` is rejected with a 403.

The demo gets its JWT from a dev-only `/dev-token` endpoint built into the Vite dev server; see [example/README.md](example/README.md).

---

## Security Notice

Orbit has the v0.1 security baseline: signed tokens, deny-by-default channel ACLs, an origin allowlist, and connection limits. Before deploying to production:

- **Set strong secrets.** `ORBIT_JWT_SECRET` (32+ characters) and `ORBIT_SERVER_SECRET` should be random, e.g. `openssl rand -hex 32`. The server refuses to start without them. Never reuse the dev values from `docker-compose.yml`, and never send either secret to browsers.
- **Mint JWTs on your backend.** Tokens must be HS256 with `sub` and `exp`. Grant the narrowest `channels.subscribe` / `channels.publish` patterns you can; a missing claim means no access. Keep `exp` short (an hour or less). Expiry is checked when a connection opens, and an open connection is closed with code `4001` when its token expires, so clients must reconnect with a fresh token.
- **Set `ORBIT_ALLOWED_ORIGINS`** to the host patterns of your web apps. Unset means same-origin only.
- **Tune the rate limits.** New connections are limited per IP (`ORBIT_RATE_LIMIT_CONNS_PER_SEC`) and per user (`ORBIT_MAX_CONNS_PER_USER`). Set `ORBIT_TRUSTED_PROXY=true` only behind a proxy you control, otherwise clients can spoof `X-Forwarded-For`.
- **Presence endpoints need auth.** `GET /api/presence` and `/api/presence/count` accept the server secret or a client JWT whose subscribe ACL covers the channel, and only echo CORS headers for `ORBIT_ALLOWED_ORIGINS`.
- **No TLS** — run behind a reverse proxy (nginx, Caddy) that handles HTTPS/WSS termination.

---

## Roadmap

Orbit's thesis: **self-hosted realtime infrastructure with strong presence primitives.**

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

---

> **Scope guard:** Orbit is meant to be understandable in an afternoon. Features that require a configuration file longer than 50 lines belong in a different project.

## Non-goals

Orbit is not, and does not intend to become:

- A general-purpose durable streaming system designed for event sourcing
- A general message queue like RabbitMQ
- A hosted-platform clone of Ably or Pusher
- A multi-transport client delivery system — WebSocket is the only client delivery transport; HTTP publish exists only as a server-side ingress API
- A product that requires a config file longer than 50 lines
- A system that guarantees exactly-once delivery

If you need any of the above, use the right tool for the job.

---

## Contributing

Contributions are welcome! Please read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a PR.

Bug reports and feature requests → [GitHub Issues](https://github.com/JabezSanjay/orbit/issues)

---

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for release history.

---

## License

[MIT](LICENSE) — free to use, modify, and distribute.
