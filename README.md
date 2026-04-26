# orbit

An open-source, self-hosted realtime event mesh written in Go, powered by Redis and WebSockets.

## Architecture

*   **WebSocket Gateway**: Handles high-concurrency client connections with strict heartbeats.
*   **Redis PubSub Engine**: Used for low-latency fan-out of messages across horizontally scaled Orbit nodes.
*   **Presence Tracker**: Tracks user online status per channel using Redis Sets with TTLs to prevent ghost users.
*   **JS SDK**: Simple interface for publishers and subscribers.

## Quickstart

1.  Start the cluster using Docker Compose:
    ```bash
    docker-compose up --build
    ```
    This spins up a Redis instance and the Orbit server on port `8080`.

2.  Open `sdk/js/index.html` in your browser to interact with the event mesh.

## Example Usage (JS)

```javascript
import { Orbit } from './orbit.js';

// Connect with a token (optional auth)
const orbit = new Orbit('ws://localhost:8080/ws?token=mysecretToken');

orbit.onConnected(() => {
    console.log("Connected to Orbit!");

    // Subscribe to a channel
    orbit.subscribe('room-1', (message) => {
        console.log('Received:', message.payload);
    });

    // Publish to a channel
    orbit.publish('room-1', {
        event: 'message.created',
        payload: { text: 'Hello, World!' }
    });
});
```

## Observability

Monitor real-time metrics at `http://localhost:8080/metrics`
