# Orbit live cursors demo

A React + Vite app that shares cursor positions over the `live-canvas` channel. Open it in two browser tabs and move the mouse.

## Run it

1. Start Orbit with Redis from the repo root. `docker compose up --build` already allows the `localhost:5173` origin. If you run the server with `go run ./cmd/server` instead, set it yourself. `ORBIT_ALLOWED_ORIGINS` takes host patterns without a scheme, so `http://localhost:5173` would be rejected:

   ```bash
   ORBIT_ALLOWED_ORIGINS=localhost:5173 \
   ORBIT_JWT_SECRET=orbit-local-dev-secret-do-not-use-in-production \
   ORBIT_SERVER_SECRET=orbit-server-secret-do-not-use-in-production \
   go run ./cmd/server
   ```

2. Start the demo:

   ```bash
   cd example
   npm install
   npm run dev
   ```

3. Open http://localhost:5173 in two tabs.

## How the demo gets a token

Orbit only accepts WebSocket connections that carry an HS256 JWT with `sub`, `exp`, and a `channels` ACL claim. The app fetches one from `GET /dev-token?user=<name>` before connecting.

That endpoint is a dev-only Vite middleware in `vite.config.js`. It signs tokens with `ORBIT_JWT_SECRET` from your shell, or the public dev secret if that is unset, so it must match the secret the server runs with. The plugin uses `apply: 'serve'`, so it is not part of `npm run build` output.

**In production, your own backend mints the token** after it authenticates the user, and the signing secret never reaches the browser. See the main [README](../README.md) for the claim format.

## Scripts

| Command | What it does |
|---|---|
| `npm run dev` | Vite dev server on :5173, with the `/dev-token` middleware |
| `npm run build` | Production build into `dist/` (no token endpoint) |
| `npm run lint` | ESLint |
