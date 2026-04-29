const { Orbit } = require('../example/src/orbit.js');
const jwt = require('jsonwebtoken');
// Quick adaptation for node:
global.WebSocket = require('ws');

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
