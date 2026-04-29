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

```bash
docker-compose up --build
```

**Connect:**
```javascript
import { Orbit } from './sdk/js/orbit.js';
const orbit = new Orbit("ws://localhost:8080/ws?token=guest")
```

**Subscribe:**
```javascript
orbit.subscribe("room-1", (message) => {
    console.log("Received:", message.payload)
})
```

**Publish:**
```javascript
orbit.publish("room-1", { text: "Hello!" })
```

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
| `ORBIT_ALLOWED_ORIGINS` | _(same-origin only)_ | Comma-separated list of allowed WebSocket origins (e.g. `http://localhost:5173,https://myapp.com`) |
| `ORBIT_JWT_SECRET` | _(required)_ | HS256 signing secret — minimum 32 characters. Server refuses to start if unset or too short. |
| `ORBIT_RATE_LIMIT_CONNS_PER_SEC` | `10` | Max new WebSocket connections per second per IP. Set to `0` to disable (e.g. for load testing). |
| `ORBIT_MAX_CONNS_PER_USER` | `10` | Max simultaneous WebSocket connections per userID. Set to `0` to disable. |
| `ORBIT_TRUSTED_PROXY` | `false` | Set to `true` to trust `X-Forwarded-For` / `X-Real-IP` for IP rate limiting (use only behind a trusted reverse proxy) |

---

## HTTP API

| Method | Path | Description |
|---|---|---|
| `GET` | `/ws?token=<token>` | WebSocket upgrade endpoint |
| `GET` | `/api/presence?channel=<channel>` | Returns JSON array of active user IDs in a channel |
| `GET` | `/metrics` | Prometheus metrics scrape endpoint |

---

## WebSocket Protocol

All messages use a single JSON envelope:

```json
{
  "type": "subscribe | unsubscribe | publish | message | ping | pong",
  "channel": "room-1",
  "event": "my-event",
  "payload": {}
}
```

**Client → Server**

| `type` | Purpose |
|---|---|
| `subscribe` | Join a channel |
| `unsubscribe` | Leave a channel |
| `publish` | Broadcast `event` + `payload` to all channel subscribers |
| `ping` | Heartbeat — server replies with `pong` and refreshes presence TTL |

**Server → Client**

| `type` | Purpose |
|---|---|
| `message` | Incoming broadcast from a subscribed channel |
| `pong` | Heartbeat response |
| `error` | Access control rejection or bad request |

**Built-in presence events** (delivered as `type: "message"`):

| `event` | When |
|---|---|
| `presence.joined` | A user subscribes to a channel. `payload.user` = userID |
| `presence.left` | A user disconnects or unsubscribes. `payload.user` = userID |

---

## Demo

The `example/` directory contains a React + Vite live cursor app — open two browser tabs and see cursors move in real time.

```bash
cd example
npm install
npm run dev
```

---

## ⚠️ Security Notice (Pre-Production)

The current release is an **MVP**. Before deploying to production:

- **Set a strong `ORBIT_JWT_SECRET`** — minimum 32 characters, generated randomly (e.g. `openssl rand -hex 32`). The server refuses to start without it.
- **Issue short-lived JWTs** — set `exp` to 1 hour or less from your backend. Orbit enforces expiry on every connection.
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
