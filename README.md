<div align="center">
  <h1>Orbit</h1>
  <p><strong>Self-hosted Pusher alternative in Go.</strong></p>
  <p>Realtime messaging, zero friction.</p>

  <a href="https://golang.org/"><img src="https://img.shields.io/badge/Made%20with-Go-1f425f.svg" alt="Made with Go"></a>
  <a href="https://redis.io/"><img src="https://img.shields.io/badge/Powered%20by-Redis-dc382d.svg" alt="Powered by Redis"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License"></a>
  <a href="https://github.com/orbit/orbit/actions/workflows/ci.yml"><img src="https://github.com/orbit/orbit/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
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

---

## Demo

Two browser tabs. 
Open both. 
Publish from one. 
See live sync instantly.

*(To run the full Vite+React demo locally, run `npm run dev` in the `/example` directory).*

---

## Roadmap

- [x] Core PubSub MVP
- [x] Presence MVP
- [x] Metrics instrumentation
- [ ] Redis Streams adapter
- [ ] NATS adapter
