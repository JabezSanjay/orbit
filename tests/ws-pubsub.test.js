const fs = require('fs');
const WebSocket = require('ws');

const ws = new WebSocket('ws://localhost:8080/ws?token=testuser');

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
