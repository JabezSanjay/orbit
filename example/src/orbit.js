// Live member list of one channel, maintained from presence events.
//
// The server sends presence.state (a snapshot) after every subscribe, then
// presence.joined / rejoined / updated / left deltas. Deltas that arrive before
// the snapshot are buffered and re-applied on top of it, per user, last event
// wins — which is exactly the ordering contract the server documents.
class OrbitPresence {
    constructor(channel) {
        this.channel = channel;
        this.members = new Map(); // userID -> metadata object
        this.synced = false;      // true once the current subscription's snapshot arrived
        this._pending = new Map(); // userID -> last delta received before the snapshot
        this._listeners = new Set();
    }

    get count() {
        return this.members.size;
    }

    // onChange(cb) calls cb(members) on every change (and immediately if a
    // snapshot was already received). Returns an unsubscribe function.
    onChange(cb) {
        this._listeners.add(cb);
        if (this.synced) cb(this.members);
        return () => this._listeners.delete(cb);
    }

    _beginSync() {
        this.synced = false;
        this._pending = new Map();
    }

    _clear() {
        this.members = new Map();
        this._beginSync();
        this._emit();
    }

    _handle(msg) {
        const payload = msg.payload || {};
        switch (msg.event) {
            case "presence.state":
                this.members = new Map((payload.users || []).map(u => [u.user, u.metadata || {}]));
                for (const delta of this._pending.values()) this._apply(delta);
                this._pending = new Map();
                this.synced = true;
                this._emit();
                return;
            case "presence.joined":
            case "presence.rejoined":
            case "presence.updated":
            case "presence.left":
                if (!this.synced) {
                    this._pending.delete(payload.user); // keep insertion order = arrival order
                    this._pending.set(payload.user, msg);
                    return;
                }
                this._apply(msg);
                this._emit();
                return;
        }
    }

    _apply(msg) {
        const payload = msg.payload || {};
        if (msg.event === "presence.left") {
            this.members.delete(payload.user);
        } else {
            this.members.set(payload.user, payload.metadata || {});
        }
    }

    _emit() {
        this._listeners.forEach(cb => cb(this.members));
    }
}

export class Orbit {
    constructor(url) {
        this.url = url;
        this.ws = null;
        this.handlers = new Map();    // channel -> Set of callbacks
        this.subOptions = new Map();  // channel -> { ttl, metadata } resent on reconnect
        this.presences = new Map();   // channel -> OrbitPresence
        this.errorHandlers = new Set();
        this.onConnectedCallback = null;
        this.pingInterval = null;
        this.intentionalClose = false;
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

            if (data.type === 'error') {
                const message = (data.payload && data.payload.error) || "unknown error";
                if (this.errorHandlers.size === 0) {
                    console.warn("[Orbit] Server error:", message);
                }
                this.errorHandlers.forEach(cb => cb(message, data));
                return;
            }

            if (data.type === 'message') {
                const channel = data.channel;
                const presence = this.presences.get(channel);
                if (presence) {
                    presence._handle(data);
                }
                if (this.handlers.has(channel)) {
                    this.handlers.get(channel).forEach(cb => cb(data));
                }
            }
        };

        this.ws.onclose = () => {
            clearInterval(this.pingInterval);
            if (!this.intentionalClose) {
                console.log("[Orbit] Disconnected. Reconnecting in 3s...");
                setTimeout(() => this.connect(), 3000);
            } else {
                console.log("[Orbit] Connection closed intentionally.");
            }
        };
    }

    disconnect() {
        this.intentionalClose = true;
        if (this.ws) {
            this.ws.close();
        }
        clearInterval(this.pingInterval);
    }

    onConnected(cb) {
        this.onConnectedCallback = cb;
        // If already connected when this is called
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            cb();
        }
    }

    // onError(cb) calls cb(message, frame) for every server error frame
    // (ACL denial, invalid ttl/metadata, rate limit, ...). Returns an
    // unsubscribe function.
    onError(cb) {
        this.errorHandlers.add(cb);
        return () => this.errorHandlers.delete(cb);
    }

    // subscribe(channel, handler, { ttl, metadata }) — options apply when the
    // channel is first subscribed and are resent on every reconnect.
    subscribe(channel, handler, options = {}) {
        if (!this.handlers.has(channel)) {
            this.handlers.set(channel, new Set());
            this.subOptions.set(channel, { ...options });
            if (!this.presences.has(channel)) {
                this.presences.set(channel, new OrbitPresence(channel));
            }
            if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                this._sendSubscribe(channel);
            }
        }
        if (handler) {
            this.handlers.get(channel).add(handler);
        }
    }

    unsubscribe(channel, handler) {
        if (!this.handlers.has(channel)) return;

        const channelHandlers = this.handlers.get(channel);
        channelHandlers.delete(handler);

        if (channelHandlers.size === 0) {
            this.handlers.delete(channel);
            this.subOptions.delete(channel);
            // Keep the helper (and its onChange listeners) for a later
            // resubscribe; it is empty until the next snapshot.
            const presence = this.presences.get(channel);
            if (presence) {
                presence._clear();
            }
            if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                this.sendRaw({ type: "unsubscribe", channel });
            }
        }
    }

    publish(channel, payload) {
        this.sendRaw({
            type: "publish",
            channel: channel,
            payload: payload
        });
    }

    // updatePresence replaces this user's presence metadata on a subscribed
    // channel (null clears it); everyone receives presence.updated. The new
    // metadata is also used when resubscribing after a reconnect.
    updatePresence(channel, metadata) {
        const options = this.subOptions.get(channel);
        if (options) {
            options.metadata = metadata;
        }
        this.sendRaw({ type: "presence_update", channel, metadata });
    }

    // presence(channel) returns the live member list for a subscribed
    // channel: { members: Map(userID -> metadata), count, synced, onChange(cb) }.
    presence(channel) {
        if (!this.presences.has(channel)) {
            this.presences.set(channel, new OrbitPresence(channel));
        }
        return this.presences.get(channel);
    }

    _sendSubscribe(channel) {
        const options = this.subOptions.get(channel) || {};
        const msg = { type: "subscribe", channel: channel };
        if (options.ttl) {
            msg.ttl = options.ttl;
        }
        if (options.metadata !== undefined && options.metadata !== null) {
            msg.metadata = options.metadata;
        }
        const presence = this.presences.get(channel);
        if (presence) {
            presence._beginSync();
        }
        this.sendRaw(msg);
    }

    sendRaw(msg) {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify(msg));
        } else {
            console.warn("[Orbit] Cannot send message, WebSocket is not open", msg);
        }
    }
}
