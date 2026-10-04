import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import process from 'node:process'
import jwt from 'jsonwebtoken'

// Public dev secret, matching docker-compose.yml and tests/. Never use it in production.
const DEV_JWT_SECRET = 'orbit-local-dev-secret-do-not-use-in-production'

// DEV-ONLY token endpoint: GET /dev-token?user=<name> -> { token }
//
// Orbit authenticates WebSocket connections with an HS256 JWT. In a real app,
// YOUR backend mints that token after it has authenticated the user, and the
// signing secret never leaves the server. This middleware stands in for that
// backend so the demo works out of the box. `apply: 'serve'` keeps it out of
// `vite build` output entirely.
function orbitDevToken() {
  return {
    name: 'orbit-dev-token',
    apply: 'serve',
    configureServer(server) {
      const secret = process.env.ORBIT_JWT_SECRET || DEV_JWT_SECRET
      server.middlewares.use('/dev-token', (req, res) => {
        const url = new URL(req.url, 'http://localhost')
        const user = url.searchParams.get('user')
        res.setHeader('Content-Type', 'application/json')
        if (!user) {
          res.statusCode = 400
          res.end(JSON.stringify({ error: 'missing user parameter' }))
          return
        }
        const token = jwt.sign(
          { sub: user, channels: { subscribe: ['live-canvas'], publish: ['live-canvas'] } },
          secret,
          { algorithm: 'HS256', expiresIn: '1h' },
        )
        res.end(JSON.stringify({ token }))
      })
    },
  }
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), orbitDevToken()],
})
