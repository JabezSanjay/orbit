import { createRequire } from 'module';
import { Orbit } from '../example/src/orbit.js';

const require = createRequire(import.meta.url);
const jwt = require('jsonwebtoken');
const ws = require('ws');

// Polyfill WebSocket for Node.js
global.WebSocket = ws;

const SECRET = process.env.ORBIT_JWT_SECRET || 'orbit-local-dev-secret-do-not-use-in-production';
const token = jwt.sign(
  { sub: 'testuser', channels: { subscribe: ['*'], publish: ['*'] } },
  SECRET,
  { algorithm: 'HS256', expiresIn: '1h' }
);

const orbit = new Orbit(`ws://localhost:8080/ws?token=${token}`);
orbit.onConnected(() => {
    orbit.subscribe('global-hub', (msg) => {
        console.log("MSG EVENT:", msg.event);
        console.log("MSG PAYLOAD TYPE:", typeof msg.payload);
        console.log("USER:", msg.payload.user);
        if (msg.event === 'presence.joined') process.exit(0);
    });
});

setTimeout(() => {
    console.error("Timeout: presence.joined not received");
    process.exit(1);
}, 5000);
