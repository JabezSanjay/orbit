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

- **Auth is a stub** — `token` query param maps directly to a userID. Replace `TokenAuthenticator` in `internal/auth/auth.go` with real JWT or HMAC validation.
- **`InsecureSkipVerify: true`** is set on WebSocket accept for local dev. Remove this for production.
- **No TLS** — run behind a reverse proxy (nginx, Caddy) that handles HTTPS/WSS termination.

---

## Roadmap

Orbit ships in four phases. The goal is to stay simple, not to become Centrifugo.

### Phase 1 — Stable Alpha *(security + correctness)*
> Fix every blocker before any public deployment.

- [ ] Replace auth stub with real JWT / HMAC token validation
- [ ] Remove `InsecureSkipVerify` from WebSocket accept
- [ ] Implement `CanSubscribe` / `CanPublish` channel-level ACLs
- [ ] Fix JS SDK `unsubscribe()` to send an unsubscribe frame to the server
- [ ] Harden CI: reliable server-ready health check before integration tests

### Phase 2 — Public Beta *(production-readiness)*
> Safe to run in real environments.

- [ ] Message history + replay via Redis Streams (`XADD` / `XREAD`)
- [ ] Per-namespace channel config (custom TTLs, ACL rules, history depth)
- [ ] Connection rate limiting and per-user connection caps
- [ ] TLS termination guide + example nginx / Caddy configs
- [ ] Python SDK
- [ ] Go client SDK

### Phase 3 — v1.0 *(developer experience)*
> Polished, well-documented, observable.

- [ ] Read-only admin dashboard (active connections, channels, message rates)
- [ ] Official Docker Hub image + versioned releases
- [ ] Helm chart for Kubernetes deployments
- [ ] Structured JSON logging with configurable log levels
- [ ] Graceful shutdown with in-flight message draining

### Phase 4 — Ecosystem *(platform features)*
> Compete with hosted platforms on features, not just on price.

- [ ] REST publish API — publish to a channel over HTTP without a WebSocket connection
- [ ] Webhooks — fire HTTP callbacks on publish / presence events
- [ ] SSE fallback for clients that block WebSockets
- [ ] Multi-tenant namespace isolation
- [ ] Official Grafana dashboard (pre-built, importable)

---

> **Scope guard:** Orbit is meant to be understandable in an afternoon. Features that require a configuration file longer than 50 lines belong in a different project.

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

---

## Roadmap

- [x] Core PubSub MVP
- [x] Presence MVP
- [x] Metrics instrumentation
- [ ] Real JWT authentication
- [ ] Channel-level ACLs
- [ ] Redis Streams adapter
- [ ] NATS adapter

Quick Demo
<img width="3398" height="2038" alt="Screen Recording Apr 28 2026 from CloudConvert" src="https://github.com/user-attachments/assets/24dc76f5-1b23-48dd-bd9a-be27b3162e8e" />
