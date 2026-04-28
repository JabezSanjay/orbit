const { Orbit } = require('../example/src/orbit.js');
// Quick adaptation for node:
global.WebSocket = require('ws');
const orbit = new Orbit('ws://localhost:8080/ws?token=test');
orbit.onConnected(() => {
    orbit.subscribe('global-hub', (msg) => {
        console.log("MSG EVENT:", msg.event);
        console.log("MSG PAYLOAD TYPE:", typeof msg.payload);
        console.log("USER:", msg.payload.user);
        if (msg.event === 'presence.joined') process.exit(0);
    });
});
