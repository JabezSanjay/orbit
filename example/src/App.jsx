import React, { useState, useEffect, useRef } from 'react';
import { Orbit } from './orbit.js';
import './App.css';

const CHANNEL = 'global-hub';
const MY_USER = 'Guest-' + Math.random().toString(36).substring(7);

function App() {
  const [users, setUsers] = useState([]);
  const [messages, setMessages] = useState([]);
  const [inputValue, setInputValue] = useState('');
  const [isConnected, setIsConnected] = useState(false);
  const orbitRef = useRef(null);
  const messagesEndRef = useRef(null);

  useEffect(() => {
    // 1. Fetch Initial Presence Array
    fetch(`http://localhost:8080/api/presence?channel=${CHANNEL}`)
      .then(res => res.json())
      .then(data => {
        if (data.users) {
          const strippedUsers = data.users.map(u => u.startsWith('user_') ? u.substring(5) : u);
          setUsers(strippedUsers);
        }
      })
      .catch(err => console.error("Could not fetch presence", err));

    // 2. Connect to Orbit Mesh
    const orbit = new Orbit(`ws://localhost:8080/ws?token=${MY_USER}`);
    orbitRef.current = orbit;

    orbit.onConnected(() => setIsConnected(true));

    // 3. Subscribe to the channel
    orbit.subscribe(CHANNEL, (msg) => {
      if (msg.event === 'presence.joined') {
        try {
          let userObj = msg.payload;
          if (typeof userObj === 'string') {
              try { userObj = JSON.parse(userObj); } catch(e) {}
          }
          let user = userObj?.user || 'Unknown';
          if (user.startsWith('user_')) user = user.substring(5);
          
          setUsers(prev => (prev.includes(user) ? prev : [...prev, user]));
          setMessages(prev => [...prev, { sys: true, text: `${user} landed in orbit.` }]);
        } catch(e){}
      } else if (msg.event === 'presence.left') {
        try {
          let userObj = msg.payload;
          if (typeof userObj === 'string') {
              try { userObj = JSON.parse(userObj); } catch(e) {}
          }
          let user = userObj?.user || 'Unknown';
          if (user.startsWith('user_')) user = user.substring(5);

          setUsers(prev => prev.filter(u => u !== user));
          setMessages(prev => [...prev, { sys: true, text: `${user} left orbit.` }]);
        } catch(e){}
      } else {
        // Standard payload message
        setMessages(prev => [
          ...prev,
          {
            id: Math.random().toString(),
            user: msg.payload.user,
            text: msg.payload.text,
            isMe: msg.payload.user === MY_USER,
            timestamp: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
          }
        ]);
      }
    });

    return () => {
      orbit.disconnect();
    };
  }, []);

  useEffect(() => {
    // Auto scroll bottom
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  const handleSend = (e) => {
    e.preventDefault();
    if (!inputValue.trim()) return;

    orbitRef.current.publish(CHANNEL, {
      user: MY_USER,
      text: inputValue
    });
    setInputValue('');
  };

  return (
    <div className="orbit-app">
      {/* Sidebar: Presence Indicators */}
      <aside className="sidebar">
        <div className="sidebar-header">
          <h2>Orbit</h2>
          <span className={`status-badge ${isConnected ? 'online' : 'offline'}`}>
            {isConnected ? 'Mesh Connected' : 'Connecting...'}
          </span>
        </div>

        <div className="channel-info">
          <h3>#{CHANNEL}</h3>
          <p>{users.length} Active Users</p>
        </div>

        <div className="users-list">
          {users.map(u => (
            <div key={u} className={`user-item ${u === MY_USER ? 'you' : ''}`}>
              <div className="presence-dot pulse"></div>
              <span>{u} {u === MY_USER && '(You)'}</span>
            </div>
          ))}
        </div>
      </aside>

      {/* Main Chat Area */}
      <main className="chat-area">
        <header className="chat-header">
          <h2>Global Feed</h2>
          <p>Realtime events distributed via Redis Fanout</p>
        </header>

        <div className="messages-container">
          {messages.map((msg, idx) => {
            if (msg.sys) {
              return (
                <div key={idx} className="sys-message">
                  <span>{msg.text}</span>
                </div>
              );
            }
            return (
              <div key={msg.id} className={`message-bubble-wrapper ${msg.isMe ? 'mine' : 'theirs'}`}>
                {!msg.isMe && <div className="message-user">{msg.user}</div>}
                <div className="message-bubble">
                  {msg.text}
                </div>
                <div className="message-time">{msg.timestamp}</div>
              </div>
            );
          })}
          <div ref={messagesEndRef} />
        </div>

        <form className="message-input-area" onSubmit={handleSend}>
          <input
            type="text"
            placeholder="Broadcast a message across the mesh..."
            value={inputValue}
            onChange={(e) => setInputValue(e.target.value)}
            disabled={!isConnected}
          />
          <button type="submit" disabled={!isConnected}>
            <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <line x1="22" y1="2" x2="11" y2="13"></line>
              <polygon points="22 2 15 22 11 13 2 9 22 2"></polygon>
            </svg>
          </button>
        </form>
      </main>
    </div>
  );
}

export default App;
