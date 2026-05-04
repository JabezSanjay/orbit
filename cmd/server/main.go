package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/orbit/orbit/internal/auth"
	"github.com/orbit/orbit/internal/presence"
	"github.com/orbit/orbit/internal/pubsub"
	"github.com/orbit/orbit/internal/ratelimit"
	"github.com/orbit/orbit/internal/router"
	"github.com/orbit/orbit/internal/ws"
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

	// Allowed origins for WebSocket upgrades (comma-separated)
	var allowedOrigins []string
	if raw := os.Getenv("ORBIT_ALLOWED_ORIGINS"); raw != "" {
		for _, o := range strings.Split(raw, ",") {
			if o = strings.TrimSpace(o); o != "" {
				allowedOrigins = append(allowedOrigins, o)
			}
		}
	}

	// Rate limiting config
	rateLimitConns := envInt("ORBIT_RATE_LIMIT_CONNS_PER_SEC", 10)
	maxConnsPerUser := envInt("ORBIT_MAX_CONNS_PER_USER", 10)
	trustedProxy := os.Getenv("ORBIT_TRUSTED_PROXY") == "true"
	ipLimiter := ratelimit.NewIPRateLimiter(rateLimitConns, trustedProxy)

	// Slow consumer config
	clientBufSize := envInt("ORBIT_CLIENT_BUFFER_SIZE", 256)
	slowClientThreshold := envInt("ORBIT_SLOW_CLIENT_THRESHOLD", 50)

	// Shutdown config
	shutdownTimeout := envDuration("ORBIT_SHUTDOWN_TIMEOUT", 10*time.Second)
	var shuttingDown atomic.Bool

	// 2. Initialize Core Services
	authenticator := auth.NewJWTAuthenticator(jwtSecret)
	presenceTTL := time.Duration(envInt("ORBIT_PRESENCE_TTL_SECONDS", 45)) * time.Second
	tracker := presence.NewTracker(redisClient)

	gateway := ws.NewGateway(maxConnsPerUser)
	go gateway.Run()

	msgRouter := router.NewDefaultRouter(pubsubEngine, tracker, gateway, presenceTTL)

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
		userID, perms, err := authenticator.Authenticate(r)
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
			OriginPatterns: allowedOrigins,
		})
		if err != nil {
			log.Printf("WS Upgrade Error: %v", err)
			return
		}

		id := uuid.New().String()
		client := ws.NewClient(id, userID, perms, conn, gateway, msgRouter, clientBufSize, slowClientThreshold)
		
		gateway.Register <- client

		go client.WritePump()
		client.ReadPump()
	})

	// Add metrics endpoint via Prometheus
	mux.Handle("/metrics", promhttp.Handler())

	// Add presence endpoint
	mux.HandleFunc("/api/presence", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")

		channel := r.URL.Query().Get("channel")
		if channel == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"missing channel parameter"}`))
			return
		}

		entries, err := tracker.GetUsersWithMetadata(r.Context(), channel)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"internal server error"}`))
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"channel": channel,
			"users":   entries,
		})
	})

	// REST publish endpoint — allows backend services to publish to a channel over HTTP.
	mux.HandleFunc("/api/publish", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			w.Write([]byte(`{"ok":false,"error":"method not allowed"}`))
			return
		}

		// Bearer token auth against ORBIT_SERVER_SECRET.
		authhdr := r.Header.Get("Authorization")
		if len(authhdr) < 8 || authhdr[:7] != "Bearer " || authhdr[7:] != serverSecret {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"ok":false,"error":"unauthorized"}`))
			return
		}

		var body struct {
			Channel string          `json:"channel"`
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"error":"invalid JSON body"}`))
			return
		}
		if body.Channel == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"error":"channel is required"}`))
			return
		}

		envelope, _ := json.Marshal(map[string]interface{}{
			"type":    "publish",
			"channel": body.Channel,
			"event":   body.Event,
			"payload": body.Payload,
		})
		if err := pubsubEngine.Publish(r.Context(), body.Channel, envelope); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"ok":false,"error":"internal server error"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	// Occupancy count endpoint
	mux.HandleFunc("/api/presence/count", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")

		channel := r.URL.Query().Get("channel")
		if channel == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"missing channel parameter"}`))
			return
		}

		count, err := tracker.Count(r.Context(), channel)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"internal server error"}`))
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"channel": channel,
			"count":   count,
		})
	})

	// Optionally map sdk files for dev usage
	mux.Handle("/", http.FileServer(http.Dir("./sdk/js")))

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
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
