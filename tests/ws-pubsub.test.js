const WebSocket = require('ws');
const jwt = require('jsonwebtoken');

const SECRET = process.env.ORBIT_JWT_SECRET || 'orbit-local-dev-secret-do-not-use-in-production';
const token = jwt.sign(
  { sub: 'testuser', channels: { subscribe: ['*'], publish: ['*'] } },
  SECRET,
  { algorithm: 'HS256', expiresIn: '1h' }
);

const ws = new WebSocket(`ws://localhost:8080/ws?token=${token}`);

ws.on('open', () => {
  ws.send(JSON.stringify({ type: 'subscribe', channel: 'room-1' }));
  
  setTimeout(() => {
    ws.send(JSON.stringify({
      type: 'publish',
      channel: 'room-1',
      event: 'test',
      payload: { text: "hello" }
    }));
  }, 100);
});

let cnt = 0;
ws.on('message', (data) => {
  cnt++;
  console.log(`Msg ${cnt}: `, data.toString());
  
  if (cnt >= 2) {
    // Assuming 1 publish means 1 response. If we get 2, it's double.
    setTimeout(() => {
        ws.close();
        process.exit();
    }, 500);
  }
});

setTimeout(() => {
    console.log("Timeout, total received:", cnt);
    ws.close();
    process.exit();
}, 2000);
