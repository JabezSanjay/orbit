export class Orbit {
    constructor(url) {
        this.url = url;
        this.ws = null;
        this.handlers = new Map(); // channel -> Set of callbacks
        this.onConnectedCallback = null;
        this.pingInterval = null;
        this.connect();
    }

    connect() {
        if (this.ws) {
            this.ws.close();
        }
        this.ws = new WebSocket(this.url);

        this.ws.onopen = () => {
            console.log("[Orbit] Connected to mesh.");
            if (this.onConnectedCallback) {
                this.onConnectedCallback();
            }

            // Start heartbeat
            this.pingInterval = setInterval(() => {
                this.sendRaw({ type: "ping" });
            }, 10000); // 10s ping, server expects within 15s

            // Resubscribe all channels
            for (const channel of this.handlers.keys()) {
                this._sendSubscribe(channel);
            }
        };

        this.ws.onmessage = (event) => {
            const data = JSON.parse(event.data);
            
            if (data.type === 'pong') {
                return;
            }

            if (data.type === 'message') {
                const channel = data.channel;
                if (this.handlers.has(channel)) {
                    this.handlers.get(channel).forEach(cb => cb(data));
                }
            }
        };

        this.ws.onclose = () => {
            console.log("[Orbit] Disconnected. Reconnecting in 3s...");
            clearInterval(this.pingInterval);
            setTimeout(() => this.connect(), 3000);
        };
    }

    onConnected(cb) {
        this.onConnectedCallback = cb;
        // If already connected when this is called
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            cb();
        }
    }

    subscribe(channel, handler) {
        if (!this.handlers.has(channel)) {
            this.handlers.set(channel, new Set());
            if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                this._sendSubscribe(channel);
            }
        }
        this.handlers.get(channel).add(handler);
    }

    unsubscribe(channel, handler) {
        if (!this.handlers.has(channel)) return;
        
        const channelHandlers = this.handlers.get(channel);
        channelHandlers.delete(handler);
        
        if (channelHandlers.size === 0) {
            this.handlers.delete(channel);
            // Optional: send unsubscribe event to server if we want strict resource freeing
        }
    }

    publish(channel, payload) {
        this.sendRaw({
            type: "publish",
            channel: channel,
            payload: payload
        });
    }

    _sendSubscribe(channel) {
        this.sendRaw({
            type: "subscribe",
            channel: channel
        });
    }

    sendRaw(msg) {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify(msg));
        } else {
            console.warn("[Orbit] Cannot send message, WebSocket is not open", msg);
        }
    }
}
