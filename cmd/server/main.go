package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/orbit/orbit/internal/auth"
	"github.com/orbit/orbit/internal/presence"
	"github.com/orbit/orbit/internal/pubsub"
	"github.com/orbit/orbit/internal/router"
	"github.com/orbit/orbit/internal/ws"
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

	// 2. Initialize Core Services
	authenticator := auth.NewTokenAuthenticator("secret") // Stub Secret
	tracker := presence.NewTracker(redisClient, 45*time.Second) // 45s TTL
	
	gateway := ws.NewGateway()
	go gateway.Run()

	msgRouter := router.NewDefaultRouter(authenticator, pubsubEngine, tracker, gateway)

	// 3. HTTP Handlers
	mux := http.NewServeMux()

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		userID, err := authenticator.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true, // For cross-origin dev testing
		})
		if err != nil {
			log.Printf("WS Upgrade Error: %v", err)
			return
		}

		id := uuid.New().String()
		client := ws.NewClient(id, userID, conn, gateway, msgRouter)
		
		gateway.Register <- client

		go client.WritePump()
		client.ReadPump()
	})

	// Add metrics endpoint
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		metrics := map[string]interface{}{
			"active_connections": gateway.Metrics.ActiveConnections,
			"messages_published": msgRouter.GetMetrics().MessagesPublished,
		}
		json.NewEncoder(w).Encode(metrics)
	})

	// Add presence endpoint
	mux.HandleFunc("/api/presence", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		
		channel := r.URL.Query().Get("channel")
		if channel == "" {
			http.Error(w, `{"error":"missing channel parameter"}`, http.StatusBadRequest)
			return
		}

		users, err := tracker.GetUsers(r.Context(), channel)
		if err != nil {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
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
