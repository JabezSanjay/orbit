/**
 * Integration test: REST publish API
 *
 * Verifies that POSTing to /api/publish delivers a message to a connected
 * WebSocket subscriber on the same channel.
 *
 * Prerequisites:
 *   - Orbit server running on localhost:8080
 *   - ORBIT_JWT_SECRET and ORBIT_SERVER_SECRET set (or use defaults below)
 *
 * Run:
 *   node tests/rest-publish.test.mjs
 */

import WebSocket from 'ws';
import jwt from 'jsonwebtoken';

const JWT_SECRET = process.env.ORBIT_JWT_SECRET || 'orbit-local-dev-secret-do-not-use-in-production';
const SERVER_SECRET = process.env.ORBIT_SERVER_SECRET || 'orbit-server-secret-do-not-use-in-production';
const BASE_URL = process.env.ORBIT_URL || 'http://localhost:8080';
const WS_URL = BASE_URL.replace(/^http/, 'ws');

const CHANNEL = `rest-publish-test-${Date.now()}`;

const token = jwt.sign(
  { sub: 'resttest', channels: { subscribe: ['*'], publish: ['*'] } },
  JWT_SECRET,
  { algorithm: 'HS256', expiresIn: '1h' }
);

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

async function run() {
  console.log('REST publish API integration test\n');

  // --- Test 1: message delivered to WS subscriber ---
  console.log('1. POST /api/publish delivers message to WebSocket subscriber');
  await new Promise((resolve) => {
    const ws = new WebSocket(`${WS_URL}/ws?token=${token}`);

    ws.once('open', () => {
      ws.send(JSON.stringify({ type: 'subscribe', channel: CHANNEL }));

      // Give the subscription a moment to register, then publish via HTTP.
      setTimeout(async () => {
        const res = await fetch(`${BASE_URL}/api/publish`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${SERVER_SECRET}`,
          },
          body: JSON.stringify({
            channel: CHANNEL,
            event: 'payment.completed',
            payload: { amount: 99.00, currency: 'USD' },
          }),
        });

        const body = await res.json();
        assert(res.status === 200, `POST /api/publish returns 200`);
        assert(body.ok === true, `response body is { ok: true }`);
      }, 150);
    });

    ws.on('message', (raw) => {
      const msg = JSON.parse(raw.toString());
      if (msg.event === 'payment.completed') {
        assert(msg.type === 'message', `received type is "message"`);
        assert(msg.channel === CHANNEL, `received channel matches`);
        assert(msg.payload?.currency === 'USD', `payload forwarded correctly`);
        ws.close();
        resolve();
      }
    });

    setTimeout(() => {
      console.error('  ✗ timed out waiting for message');
      failed++;
      ws.close();
      resolve();
    }, 3000);
  });

  // --- Test 2: missing channel returns 400 ---
  console.log('\n2. Missing channel field returns 400');
  {
    const res = await fetch(`${BASE_URL}/api/publish`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${SERVER_SECRET}`,
      },
      body: JSON.stringify({ event: 'test' }),
    });
    const body = await res.json();
    assert(res.status === 400, `status is 400`);
    assert(body.ok === false, `body.ok is false`);
  }

  // --- Test 3: wrong secret returns 401 ---
  console.log('\n3. Wrong Authorization secret returns 401');
  {
    const res = await fetch(`${BASE_URL}/api/publish`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer wrong-secret',
      },
      body: JSON.stringify({ channel: CHANNEL, event: 'test' }),
    });
    const body = await res.json();
    assert(res.status === 401, `status is 401`);
    assert(body.ok === false, `body.ok is false`);
  }

  // --- Test 4: missing Authorization header returns 401 ---
  console.log('\n4. Missing Authorization header returns 401');
  {
    const res = await fetch(`${BASE_URL}/api/publish`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ channel: CHANNEL, event: 'test' }),
    });
    const body = await res.json();
    assert(res.status === 401, `status is 401`);
    assert(body.ok === false, `body.ok is false`);
  }

  // --- Test 5: wrong HTTP method returns 405 ---
  console.log('\n5. GET /api/publish returns 405');
  {
    const res = await fetch(`${BASE_URL}/api/publish`, { method: 'GET' });
    assert(res.status === 405, `status is 405`);
  }

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch((err) => {
  console.error('Unexpected error:', err);
  process.exit(1);
});
