#!/usr/bin/env node
// Mint a short-lived HS256 JWT for local development.
//
//   npm run token -- <user-id> [channel-patterns]
//   node scripts/dev-token.mjs alice
//   node scripts/dev-token.mjs alice 'room-*,notifications-alice'
//
// channel-patterns is a comma-separated list applied to both the subscribe
// and publish ACLs (default "*"). Patterns are an exact name, a trailing
// wildcard ("room-*"), or "*".
//
// Signs with ORBIT_JWT_SECRET, falling back to the public dev secret used by
// docker-compose.yml and tests/. DEV ONLY: in production your own backend
// mints tokens after authenticating the user; never ship the secret to clients.

import jwt from 'jsonwebtoken';

const DEV_SECRET = 'orbit-local-dev-secret-do-not-use-in-production';

const [user, channelsArg = '*'] = process.argv.slice(2);

if (!user || user === '-h' || user === '--help') {
  console.error('usage: npm run token -- <user-id> [channel-patterns]');
  console.error("  e.g. npm run token -- alice 'room-*,lobby'");
  process.exit(user ? 0 : 1);
}

const secret = process.env.ORBIT_JWT_SECRET || DEV_SECRET;
const channels = channelsArg.split(',').map((s) => s.trim()).filter(Boolean);

const token = jwt.sign(
  { sub: user, channels: { subscribe: channels, publish: channels } },
  secret,
  { algorithm: 'HS256', expiresIn: '1h' },
);

console.log(token);
