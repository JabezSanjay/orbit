/**
 * Integration test: per-connection presence, unsubscribe, presence.state
 * snapshot, presence_update, and the JS SDK presence helper.
 *
 * Prerequisites: an Orbit server (default http://localhost:8080). Optionally a
 * second node on the same Redis (ORBIT_URL_2) to cover cross-node refcounting.
 *
 * Env:
 *   ORBIT_URL                      base URL of node 1 (default http://localhost:8080)
 *   ORBIT_URL_2                    base URL of node 2 (optional; enables the cross-node case)
 *   ORBIT_JWT_SECRET               JWT secret of the server(s)
 *   ORBIT_PRESENCE_LEAVE_GRACE_MS  the servers' leave grace period (default 2000)
 *
 * Run:
 *   node tests/presence-multi-conn.test.mjs
 */

import WebSocket from 'ws';
import jwt from 'jsonwebtoken';
import { Orbit } from '../example/src/orbit.js';

const SECRET = process.env.ORBIT_JWT_SECRET || 'orbit-local-dev-secret-do-not-use-in-production';
const BASE_URL = process.env.ORBIT_URL || 'http://localhost:8080';
const BASE_URL_2 = process.env.ORBIT_URL_2 || '';
const GRACE_MS = Number(process.env.ORBIT_PRESENCE_LEAVE_GRACE_MS || 2000);
const SETTLE_MS = 300; // time allowed for fanout to deliver an event
const CH = `presence-multi-${Date.now()}`;

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

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function token(sub) {
  return jwt.sign({ sub, channels: { subscribe: ['presence-*'], publish: ['presence-*'] } }, SECRET, {
    algorithm: 'HS256',
    expiresIn: '1h',
  });
}

const wsURL = (base) => base.replace(/^http/, 'ws');

// connect opens a raw WebSocket and records every frame it receives.
function connect(user, base = BASE_URL) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`${wsURL(base)}/ws?token=${token(user)}`);
    const conn = {
      ws,
      user,
      frames: [],
      send: (msg) => ws.send(JSON.stringify(msg)),
      events: (event) => conn.frames.filter((f) => f.event === event),
      clear: () => { conn.frames.length = 0; },
      async waitFor(pred, timeout = 3000) {
        const deadline = Date.now() + timeout;
        while (Date.now() < deadline) {
          const hit = conn.frames.find(pred);
          if (hit) return hit;
          await sleep(20);
        }
        return null;
      },
      close: () => new Promise((r) => { ws.once('close', r); ws.close(); }),
    };
    ws.on('message', (raw) => conn.frames.push(JSON.parse(raw.toString())));
    ws.once('open', () => resolve(conn));
    ws.once('error', reject);
  });
}

async function subscribe(conn, extra = {}) {
  conn.send({ type: 'subscribe', channel: CH, ...extra });
  return conn.waitFor((f) => f.event === 'presence.state' && f.channel === CH);
}

const isUser = (event, user) => (f) => f.event === event && f.payload?.user === user;

async function presenceAPI(base = BASE_URL) {
  const res = await fetch(`${base}/api/presence?channel=${encodeURIComponent(CH)}`);
  return (await res.json()).users;
}

async function run() {
  console.log(`Presence multi-connection integration test (grace ${GRACE_MS} ms)\n`);

  console.log('1. presence.state snapshot is sent to the subscriber only');
  const bob = await connect('bob');
  let state = await subscribe(bob);
  assert(state && state.payload.users.length === 1 && state.payload.users[0].user === 'bob',
    'subscriber to an empty channel gets a snapshot containing only itself');
  bob.clear();

  const tab1 = await connect('alice');
  state = await subscribe(tab1, { metadata: { name: 'Alice' } });
  const users = Object.fromEntries((state?.payload.users || []).map((u) => [u.user, u.metadata]));
  assert(users.alice?.name === 'Alice' && users.bob && Object.keys(users.bob).length === 0,
    'snapshot lists existing members with metadata ({} when none) plus the subscriber');
  assert(await bob.waitFor(isUser('presence.joined', 'alice')), 'existing member receives presence.joined');
  assert(bob.events('presence.state').length === 0, 'existing member does not receive the snapshot');

  console.log('\n2. A second connection of the same user emits no joined');
  const tab2 = await connect('alice');
  state = await subscribe(tab2);
  await sleep(SETTLE_MS);
  assert(bob.events('presence.joined').length === 1, 'still exactly one presence.joined for alice');
  assert(state?.payload.users.length === 2, 'second tab snapshot has two users');
  const count = await (await fetch(`${BASE_URL}/api/presence/count?channel=${CH}`)).json();
  assert(count.count === 2, '/api/presence/count counts users, not connections');

  console.log('\n3. Closing one of two connections emits no left');
  await tab1.close();
  await sleep(GRACE_MS + SETTLE_MS);
  assert(bob.events('presence.left').length === 0, 'no presence.left while tab2 is connected');
  assert((await presenceAPI()).some((u) => u.user === 'alice'), 'alice still listed in /api/presence');

  console.log('\n4. presence_update broadcasts presence.updated');
  tab2.send({ type: 'presence_update', channel: CH, metadata: { status: 'away' } });
  const updated = await bob.waitFor(isUser('presence.updated', 'alice'));
  assert(updated?.payload.metadata?.status === 'away', 'peer receives presence.updated with the new metadata');
  assert((await presenceAPI()).find((u) => u.user === 'alice')?.metadata?.status === 'away',
    '/api/presence reflects the updated metadata');
  tab2.send({ type: 'presence_update', channel: 'presence-not-subscribed', metadata: { a: 1 } });
  const err = await tab2.waitFor((f) => f.type === 'error');
  assert(err?.payload?.error === 'not subscribed to this channel', 'presence_update on an unsubscribed channel returns an error');

  console.log('\n5. Closing the last connection emits exactly one left, after the grace period');
  const closedAt = Date.now();
  await tab2.close();
  const left = await bob.waitFor(isUser('presence.left', 'alice'), GRACE_MS + 3000);
  assert(left, 'presence.left received');
  assert(Date.now() - closedAt >= GRACE_MS - 50, 'presence.left waited for the grace period');
  await sleep(SETTLE_MS);
  assert(bob.events('presence.left').length === 1, 'exactly one presence.left');
  assert(!(await presenceAPI()).some((u) => u.user === 'alice'), 'alice removed from /api/presence');

  console.log('\n6. Unsubscribe stops delivery and leaves immediately');
  bob.clear();
  const carol = await connect('carol');
  await subscribe(carol);
  await bob.waitFor(isUser('presence.joined', 'carol'));
  const unsubAt = Date.now();
  carol.send({ type: 'unsubscribe', channel: CH });
  const carolLeft = await bob.waitFor(isUser('presence.left', 'carol'));
  assert(carolLeft, 'peer receives presence.left on unsubscribe');
  assert(GRACE_MS < 500 || Date.now() - unsubAt < GRACE_MS, 'unsubscribe does not wait for the grace period');
  carol.clear();
  bob.send({ type: 'publish', channel: CH, event: 'chat', payload: { text: 'hi' } });
  const chat = await bob.waitFor((f) => f.event === 'chat');
  assert(chat?.sender === 'bob', 'publish is delivered with a server-stamped sender');
  await sleep(SETTLE_MS);
  assert(carol.frames.length === 0, 'unsubscribed connection receives nothing more');
  carol.send({ type: 'unsubscribe', channel: CH }); // repeated: harmless no-op
  await sleep(SETTLE_MS);
  assert(carol.frames.length === 0, 'repeated unsubscribe is a silent no-op');
  await carol.close();

  if (BASE_URL_2) {
    console.log('\n7. Cross-node: same user on two nodes');
    bob.clear();
    const onNode1 = await connect('erin', BASE_URL);
    const onNode2 = await connect('erin', BASE_URL_2);
    const watcher2 = await connect('frank', BASE_URL_2);
    await subscribe(watcher2);
    await subscribe(onNode1);
    await subscribe(onNode2);
    await sleep(SETTLE_MS);
    assert(bob.events('presence.joined').filter((f) => f.payload.user === 'erin').length === 1,
      'one presence.joined for a user connected to both nodes');
    await onNode1.close();
    await sleep(GRACE_MS + SETTLE_MS);
    assert(bob.events('presence.left').length === 0 && watcher2.events('presence.left').length === 0,
      'closing the node-1 connection emits no left on either node');
    assert((await presenceAPI(BASE_URL_2)).some((u) => u.user === 'erin'), 'erin still listed');
    await onNode2.close();
    assert(await watcher2.waitFor(isUser('presence.left', 'erin'), GRACE_MS + 3000), 'closing the last connection emits left');
    await sleep(SETTLE_MS);
    assert(bob.events('presence.left').length === 1, 'exactly one presence.left (node-1 subscriber)');

    // Reconnect to the other node inside the grace window: node 1's leave
    // timer must find the new connection and stay silent.
    bob.clear();
    const before = await connect('gina', BASE_URL);
    await subscribe(before);
    await bob.waitFor(isUser('presence.joined', 'gina'));
    await before.close();
    const after = await connect('gina', BASE_URL_2);
    await subscribe(after);
    assert(await bob.waitFor(isUser('presence.rejoined', 'gina')), 'reconnect on another node within grace emits presence.rejoined');
    await sleep(GRACE_MS + SETTLE_MS);
    assert(bob.events('presence.left').length === 0, "the old node's leave timer emits no left after a cross-node reconnect");
    await after.close();
    await watcher2.close();
  } else {
    console.log('\n7. Cross-node: skipped (set ORBIT_URL_2 to a second node on the same Redis)');
  }

  console.log('\n8. JS SDK presence helper');
  globalThis.WebSocket = WebSocket;
  const orbit = new Orbit(`${wsURL(BASE_URL)}/ws?token=${token('dave')}`);
  const errors = [];
  orbit.onError((message) => errors.push(message));
  const room = orbit.presence(CH);
  await new Promise((resolve) => orbit.onConnected(resolve));
  orbit.subscribe(CH, () => {}, { metadata: { name: 'Dave' } });
  const deadline = Date.now() + 3000;
  while (!room.synced && Date.now() < deadline) await sleep(20);
  assert(room.synced && room.members.has('bob') && room.members.get('dave')?.name === 'Dave',
    'presence(channel) is synced from the snapshot, including own metadata');
  let changes = 0;
  room.onChange(() => changes++);
  orbit.updatePresence(CH, { name: 'Dave', status: 'busy' });
  await sleep(SETTLE_MS);
  assert(room.members.get('dave')?.status === 'busy' && changes > 0, 'updatePresence updates the member map and fires onChange');
  bob.send({ type: 'unsubscribe', channel: CH });
  await sleep(SETTLE_MS);
  assert(!room.members.has('bob'), 'presence.left removes the member');
  orbit.updatePresence('presence-not-subscribed', { a: 1 });
  await sleep(SETTLE_MS);
  assert(errors.includes('not subscribed to this channel'), 'onError receives server error frames');
  orbit.disconnect();
  await bob.close();

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch((err) => {
  console.error('Unexpected error:', err);
  process.exit(1);
});
