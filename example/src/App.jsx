import { useState, useEffect, useRef } from 'react';
import { Orbit } from './orbit.js';
import Cursor from './components/Cursor.jsx';
import './App.css';

const CHANNEL = 'live-canvas';
const MY_USER = 'Guest-' + Math.random().toString(36).substring(7).toUpperCase();

// Deterministic colors based on user string to keep consistent peer colors
const COLORS = [
  '#FF3636', '#00DF71', '#007AFF', '#FF00FF', 
  '#00E5FF', '#FF9F0A', '#BF5AF2', '#FF375F'
];

function getColorForUser(userStr) {
  let hash = 0;
  for (let i = 0; i < userStr.length; i++) {
    hash = userStr.charCodeAt(i) + ((hash << 5) - hash);
  }
  return COLORS[Math.abs(hash) % COLORS.length];
}

function App() {
  const [cursors, setCursors] = useState({});
  const [isConnected, setIsConnected] = useState(false);
  const [latency, setLatency] = useState(0);
  
  const orbitRef = useRef(null);
  const lastPublishTime = useRef(0);
  const pingIntervalRef = useRef(null);

  useEffect(() => {
    let orbit = null;
    let cancelled = false;

    function connect(token) {
      orbit = new Orbit(`ws://localhost:8080/ws?token=${encodeURIComponent(token)}`);
      orbitRef.current = orbit;

      orbit.onConnected(() => {
          setIsConnected(true);

          // Start latency pinger
          clearInterval(pingIntervalRef.current);
          pingIntervalRef.current = setInterval(() => {
             orbit.publish(CHANNEL, {
                 event: 'latency.ping',
                 user: MY_USER,
                 sentAt: Date.now()
             });
          }, 1000);
      });

      orbit.subscribe(CHANNEL, (msg) => {
        // Clean up peers strictly on Presence TTL (System events are injected at root level by the gateway)
        if (msg.event === 'presence.left') {
            const user = msg.payload?.user;
            if (user) {
                setCursors(prev => {
                    const next = { ...prev };
                    delete next[user];
                    return next;
                });
            }
        }
        // Custom events dispatched by clients are nested strictly within the payload block
        else if (msg.payload && msg.payload.event === 'latency.ping' && msg.payload.user === MY_USER) {
            // Calculate round-trip from Client -> Gateway -> Redis -> DistWorker -> Client
            setLatency(Date.now() - msg.payload.sentAt);
        }
        else if (msg.payload && msg.payload.event === 'cursor.move') {
            const { user, nx, ny } = msg.payload;
            // Drop self-bounces, we already draw our native OS cursor
            if (user === MY_USER) return;

            setCursors(prev => {
                const existing = prev[user];
                if (!existing) {
                    return { ...prev, [user]: { nx, ny, color: getColorForUser(user) }};
                }
                return { ...prev, [user]: { ...existing, nx, ny }};
            });
        }
      });
    }

    // Orbit only accepts signed JWTs (HS256, with `sub` and `exp`). The
    // /dev-token endpoint is a DEV-ONLY Vite middleware (see vite.config.js)
    // that signs one with the local dev secret. Production apps mint this token
    // on their own backend after authenticating the user; the signing secret
    // must never reach the browser.
    fetch(`/dev-token?user=${encodeURIComponent(MY_USER)}`)
      .then((res) => {
        if (!res.ok) throw new Error(`dev-token request failed: ${res.status}`);
        return res.json();
      })
      .then(({ token }) => {
        if (!cancelled) connect(token);
      })
      .catch((err) => console.error('[demo] could not get an Orbit token:', err));

    return () => {
      cancelled = true;
      clearInterval(pingIntervalRef.current);
      orbit?.disconnect();
    };
  }, []);

  const handlePointerMove = (e) => {
      if (!isConnected || !orbitRef.current) return;

      const now = Date.now();
      // Throttle exact publishing to 40ms (~25 FPS) enforcing stability
      if (now - lastPublishTime.current >= 40) { 
          lastPublishTime.current = now;
          
          // Send purely normalized screen coordinates to protect from resolution fragmentation
          const nx = e.clientX / window.innerWidth;
          const ny = e.clientY / window.innerHeight;

          orbitRef.current.publish(CHANNEL, {
              event: 'cursor.move',
              user: MY_USER, 
              nx, 
              ny
          });
      }
  };

  return (
    <div className="canvas-container" onPointerMove={handlePointerMove}>
        
        {/* Figma-style HUD */}
        <div className="hud">
            <h1 className="hud-title">Live Cursor Demo</h1>
            <div className="hud-status">
               <div className="hud-badge active">
                   <span className="dot pulse"></span>
                   {Object.keys(cursors).length + 1} Peers
               </div>
               <div className="hud-badge latency">
                   <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                       <path d="M12 20V10M18 20V4M6 20v-4" />
                   </svg>
                   {latency}ms RTL
               </div>
            </div>
        </div>

        {/* Dynamic Canvas Space */}
        {Object.entries(cursors).map(([user, data]) => {
            // Unpack normalized variables strictly against the localized viewing port dimensions
            const absX = data.nx * window.innerWidth;
            const absY = data.ny * window.innerHeight;
            
            return (
               <Cursor 
                  key={user} 
                  x={absX} 
                  y={absY} 
                  color={data.color} 
                  label={user} 
               />
            );
        })}
    </div>
  );
}

export default App;
