/**
 * Integration test: presence HTTP API auth + CORS
 *
 * Verifies that GET /api/presence and /api/presence/count require either the
 * server secret or a client JWT whose subscribe ACL covers the channel, and
 * that CORS headers are only sent for origins in ORBIT_ALLOWED_ORIGINS.
 *
 * Prerequisites:
 *   - Orbit server running (ORBIT_URL, default http://localhost:8080)
 *   - Server started with ORBIT_ALLOWED_ORIGINS containing ALLOWED_ORIGIN
 *     (default http://localhost:5173)
 *
 * Run:
 *   node tests/presence-api.test.mjs
 */

import jwt from 'jsonwebtoken';

const JWT_SECRET = process.env.ORBIT_JWT_SECRET || 'orbit-local-dev-secret-do-not-use-in-production';
const SERVER_SECRET = process.env.ORBIT_SERVER_SECRET || 'orbit-server-secret-do-not-use-in-production';
const BASE_URL = process.env.ORBIT_URL || 'http://localhost:8080';
const ALLOWED_ORIGIN = process.env.ORBIT_TEST_ALLOWED_ORIGIN || 'http://localhost:5173';
const DENIED_ORIGIN = 'https://evil.example';

const CHANNEL = `room-presence-api-${Date.now()}`;

const sign = (subscribe, opts = {}) =>
  jwt.sign({ sub: 'presencetest', channels: { subscribe, publish: [] } }, JWT_SECRET, {
    algorithm: 'HS256',
    expiresIn: '1h',
    ...opts,
  });

const matchingToken = sign(['room-*']);
const otherToken = sign(['lobby-*']);
const expiredToken = jwt.sign(
  { sub: 'presencetest', channels: { subscribe: ['*'] }, exp: Math.floor(Date.now() / 1000) - 60 },
  JWT_SECRET,
  { algorithm: 'HS256' }
);
const wrongKeyToken = jwt.sign({ sub: 'x', channels: { subscribe: ['*'] } }, 'not-the-real-secret-not-the-real-secret', {
  algorithm: 'HS256',
  expiresIn: '1h',
});

let passed = 0;
let failed = 0;

function assert(condition, label) {
  if (condition) {
    console.log(`  ✓ ${label}`);
    passed++;
  } else {
    console.error(`  ✗ ${label}`);
    failed++;
  }
}

const PATHS = ['/api/presence', '/api/presence/count'];

async function get(path, { bearer, query = '', origin } = {}) {
  const headers = {};
  if (bearer) headers.Authorization = `Bearer ${bearer}`;
  if (origin) headers.Origin = origin;
  return fetch(`${BASE_URL}${path}?channel=${encodeURIComponent(CHANNEL)}${query}`, { headers });
}

async function run() {
  console.log('Presence HTTP API auth + CORS integration test\n');

  for (const path of PATHS) {
    console.log(`\n== ${path} ==`);

    console.log('1. No credentials returns 401');
    {
      const res = await get(path);
      const body = await res.json();
      assert(res.status === 401, `status is 401 (got ${res.status})`);
      assert(body.ok === false && typeof body.error === 'string', `JSON error body`);
    }

    console.log('2. Invalid / expired / wrong-key credentials return 401');
    for (const [label, bearer] of [['garbage', 'not-a-token'], ['expired JWT', expiredToken], ['wrong-key JWT', wrongKeyToken]]) {
      const res = await get(path, { bearer });
      assert(res.status === 401, `${label}: status is 401 (got ${res.status})`);
    }

    console.log('3. Server secret returns 200');
    {
      const res = await get(path, { bearer: SERVER_SECRET });
      const body = await res.json();
      assert(res.status === 200, `status is 200 (got ${res.status})`);
      assert(body.channel === CHANNEL, `body.channel matches`);
    }

    console.log('4. JWT with matching subscribe ACL returns 200 (Bearer and ?token=)');
    {
      const res = await get(path, { bearer: matchingToken });
      assert(res.status === 200, `Bearer: status is 200 (got ${res.status})`);
      const res2 = await get(path, { query: `&token=${matchingToken}` });
      assert(res2.status === 200, `?token=: status is 200 (got ${res2.status})`);
    }

    console.log('5. JWT whose ACL does not cover the channel returns 403');
    {
      const res = await get(path, { bearer: otherToken });
      const body = await res.json();
      assert(res.status === 403, `status is 403 (got ${res.status})`);
      assert(body.ok === false, `body.ok is false`);
    }

    console.log('6. Missing channel returns 400 for an authenticated caller');
    {
      const res = await fetch(`${BASE_URL}${path}`, { headers: { Authorization: `Bearer ${SERVER_SECRET}` } });
      assert(res.status === 400, `status is 400 (got ${res.status})`);
    }

    console.log('7. CORS: allowed origin echoed, other origins get no ACAO');
    {
      const ok = await get(path, { bearer: SERVER_SECRET, origin: ALLOWED_ORIGIN });
      assert(ok.headers.get('access-control-allow-origin') === ALLOWED_ORIGIN, `allowed origin echoed`);
      assert((ok.headers.get('vary') || '').toLowerCase().includes('origin'), `Vary: Origin set`);
      const bad = await get(path, { bearer: SERVER_SECRET, origin: DENIED_ORIGIN });
      assert(bad.headers.get('access-control-allow-origin') === null, `disallowed origin gets no ACAO`);
      const none = await get(path, { bearer: SERVER_SECRET });
      assert(none.headers.get('access-control-allow-origin') === null, `no Origin gets no ACAO (no wildcard)`);
    }

    console.log('8. CORS preflight (OPTIONS)');
    {
      const res = await fetch(`${BASE_URL}${path}?channel=${CHANNEL}`, {
        method: 'OPTIONS',
        headers: {
          Origin: ALLOWED_ORIGIN,
          'Access-Control-Request-Method': 'GET',
          'Access-Control-Request-Headers': 'authorization',
        },
      });
      assert(res.status === 204, `status is 204 (got ${res.status})`);
      assert(res.headers.get('access-control-allow-origin') === ALLOWED_ORIGIN, `preflight echoes allowed origin`);
      assert((res.headers.get('access-control-allow-methods') || '').includes('GET'), `allows GET`);
      assert((res.headers.get('access-control-allow-headers') || '').toLowerCase().includes('authorization'), `allows Authorization header`);
      const bad = await fetch(`${BASE_URL}${path}`, {
        method: 'OPTIONS',
        headers: { Origin: DENIED_ORIGIN, 'Access-Control-Request-Method': 'GET' },
      });
      assert(bad.headers.get('access-control-allow-origin') === null, `preflight from disallowed origin gets no ACAO`);
    }

    console.log('9. Non-GET method returns 405');
    {
      const res = await fetch(`${BASE_URL}${path}?channel=${CHANNEL}`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${SERVER_SECRET}` },
      });
      assert(res.status === 405, `status is 405 (got ${res.status})`);
    }
  }

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch((err) => {
  console.error('Unexpected error:', err);
  process.exit(1);
});
