package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
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

	// 2. Initialize Core Services
	authenticator := auth.NewJWTAuthenticator(jwtSecret)
	tracker := presence.NewTracker(redisClient, 45*time.Second) // 45s TTL

	gateway := ws.NewGateway(maxConnsPerUser)
	go gateway.Run()

	msgRouter := router.NewDefaultRouter(pubsubEngine, tracker, gateway)

	// 3. HTTP Handlers
	mux := http.NewServeMux()

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
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
		client := ws.NewClient(id, userID, perms, conn, gateway, msgRouter)
		
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

		users, err := tracker.GetUsers(r.Context(), channel)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"internal server error"}`))
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"channel": channel,
			"users":   users,
		})
	})

	// Optionally map sdk files for dev usage
	mux.Handle("/", http.FileServer(http.Dir("./sdk/js")))

	log.Printf("Orbit core starting on :%s", port)
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
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
