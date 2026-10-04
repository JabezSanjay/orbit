package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/JabezSanjay/orbit/internal/auth"
	"github.com/JabezSanjay/orbit/internal/presence"
	"github.com/JabezSanjay/orbit/internal/pubsub"
	"github.com/JabezSanjay/orbit/internal/ratelimit"
	"github.com/JabezSanjay/orbit/internal/router"
	"github.com/JabezSanjay/orbit/internal/ws"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}

	jwtSecret := os.Getenv("ORBIT_JWT_SECRET")
	if len(jwtSecret) < 32 {
		log.Fatal("ORBIT_JWT_SECRET must be set and at least 32 characters long")
	}

	serverSecret := os.Getenv("ORBIT_SERVER_SECRET")
	if serverSecret == "" {
		log.Fatal("ORBIT_SERVER_SECRET must be set — it protects the REST publish endpoint")
	}

	// 1. Initialize Redis Engine
	pubsubEngine, err := pubsub.NewRedisEngine(redisURL)
	if err != nil {
		log.Fatalf("Failed to initialize PubSub: %v", err)
	}
	defer pubsubEngine.Close()

	// Parse REDIS_URL for the raw go-redis client used by Presence Tracker
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("Invalid REDIS URI for presence: %v", err)
	}
	redisClient := redis.NewClient(opts)

	// Allowed cross-origin browser origins (comma-separated), for both the
	// WebSocket upgrade and CORS. Entries may include a scheme or not; see
	// parseAllowedOrigins.
	allowedOrigins, wsOriginHosts := parseAllowedOrigins(os.Getenv("ORBIT_ALLOWED_ORIGINS"))

	// Rate limiting config
	rateLimitConns := envInt("ORBIT_RATE_LIMIT_CONNS_PER_SEC", 10)
	maxConnsPerUser := envInt("ORBIT_MAX_CONNS_PER_USER", 10)
	trustedProxy := os.Getenv("ORBIT_TRUSTED_PROXY") == "true"
	ipLimiter := ratelimit.NewIPRateLimiter(rateLimitConns, trustedProxy)

	// Slow consumer config
	clientBufSize := envInt("ORBIT_CLIENT_BUFFER_SIZE", 256)
	slowClientThreshold := envInt("ORBIT_SLOW_CLIENT_THRESHOLD", 50)

	// Per-connection inbound limits
	clientLimits := ws.ClientLimits{
		BufferSize:      clientBufSize,
		SlowThreshold:   slowClientThreshold,
		MaxMessageBytes: int64(envInt("ORBIT_MAX_MESSAGE_BYTES", ws.DefaultMaxMessageBytes)),
		MessagesPerSec:  envInt("ORBIT_CLIENT_MSGS_PER_SEC", 100),
	}

	// Shutdown config
	shutdownTimeout := envDuration("ORBIT_SHUTDOWN_TIMEOUT", 10*time.Second)
	var shuttingDown atomic.Bool

	// 2. Initialize Core Services
	authenticator := auth.NewJWTAuthenticator(jwtSecret)
	presenceTTL := time.Duration(envInt("ORBIT_PRESENCE_TTL_SECONDS", 45)) * time.Second
	leaveGrace := time.Duration(envInt("ORBIT_PRESENCE_LEAVE_GRACE_MS", 2000)) * time.Millisecond
	tracker := presence.NewTracker(redisClient)

	gateway := ws.NewGateway(maxConnsPerUser)
	go gateway.Run()

	msgRouter := router.NewDefaultRouter(pubsubEngine, tracker, gateway, presenceTTL, leaveGrace)

	// 3. HTTP Handlers
	mux := http.NewServeMux()

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// Reject new connections during graceful shutdown.
		if shuttingDown.Load() {
			http.Error(w, "server shutting down", http.StatusServiceUnavailable)
			return
		}

		// 1. Per-IP rate limit
		if !ipLimiter.Allow(r) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}

		// 2. Authenticate
		userID, perms, expiresAt, err := authenticator.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// 3. Per-user connection cap (checked before upgrade to avoid wasting the handshake)
		if !gateway.AllowConnection(userID) {
			http.Error(w, "too many connections for this user", http.StatusTooManyRequests)
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: wsOriginHosts,
		})
		if err != nil {
			log.Printf("WS Upgrade Error: %v", err)
			return
		}

		id := uuid.New().String()
		client := ws.NewClient(id, userID, perms, expiresAt, conn, gateway, msgRouter, clientLimits)

		gateway.Register <- client

		go client.WritePump()
		client.ReadPump()
	})

	// Add metrics endpoint via Prometheus
	mux.Handle("/metrics", promhttp.Handler())

	// HTTP API (see api.go).
	//   - Presence endpoints: server secret or a client JWT whose subscribe ACL
	//     covers the channel; CORS limited to ORBIT_ALLOWED_ORIGINS.
	//   - /api/publish: server secret only, for backend services.
	maxPublishBytes := envInt("ORBIT_MAX_PUBLISH_BYTES", 65536)
	if maxPublishBytes <= 0 {
		maxPublishBytes = 65536
	}
	api := &apiHandlers{
		tracker:         tracker,
		engine:          pubsubEngine,
		authenticator:   authenticator,
		serverSecret:    serverSecret,
		allowedOrigins:  allowedOrigins,
		maxPublishBytes: int64(maxPublishBytes),
	}
	mux.HandleFunc("/api/presence", api.presence)
	mux.HandleFunc("/api/presence/count", api.presenceCount)
	mux.HandleFunc("/api/publish", api.publish)

	// Optionally map sdk files for dev usage
	mux.Handle("/", http.FileServer(http.Dir("./sdk/js")))

	// ReadHeaderTimeout bounds slowloris-style header dribbling and IdleTimeout
	// reaps idle keep-alive connections. ReadTimeout / WriteTimeout are
	// deliberately NOT set: net/http applies them as deadlines on the
	// underlying connection, which a WebSocket upgrade inherits, so they would
	// forcibly close every long-lived WebSocket once they elapse. Request body
	// limits are enforced per handler instead (MaxBytesReader on /api/publish).
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("Orbit core starting on :%s", port)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Block until SIGTERM or SIGINT.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	log.Println("Shutdown signal received, draining...")
	shuttingDown.Store(true)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	// 1. Drain fanout worker queues so in-flight messages are delivered.
	log.Println("Draining fanout workers...")
	if err := pubsubEngine.Drain(shutdownCtx); err != nil {
		log.Printf("Fanout drain deadline exceeded: %v", err)
	}

	// 2. Send close frames to all connected WebSocket clients and wait for them to finish.
	log.Println("Closing WebSocket connections...")
	gateway.Shutdown(shutdownCtx)

	// 3. Stop accepting new HTTP requests.
	log.Println("Stopping HTTP server...")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Println("Orbit shut down cleanly.")
}

// envInt reads an integer from an environment variable, returning defaultVal if unset or invalid.
// A value of 0 is valid (e.g. to disable rate limiting).
func envInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultVal
}

// envDuration reads a time.Duration from an environment variable (e.g. "15s", "1m").
// Returns defaultVal if unset or unparseable.
func envDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultVal
}
