package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	_ "net/http/pprof" // Include basic pprof profiling
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JabezSanjay/orbit/internal/pubsub"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	redisURL := "redis://localhost:6379"
	if envUrl := os.Getenv("REDIS_URL"); envUrl != "" {
		redisURL = envUrl
	}

	engine, err := pubsub.NewRedisEngine(redisURL)
	if err != nil {
		log.Fatalf("Failed to initialize engine: %v", err)
	}

	go func() {
		log.Println("Starting PProf and Prometheus server on :6060")
		http.Handle("/metrics", promhttp.Handler())
		log.Println(http.ListenAndServe("0.0.0.0:6060", nil))
	}()

	numChannels := 10000
	log.Printf("Stress testing Orbit Redis V1 Engine with %d channels...", numChannels)

	var msgCounter int64

	// Concurrently subscribe to 10k channels to ensure RWMutex isn't choking
	var wg sync.WaitGroup
	for i := 0; i < numChannels; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			chName := fmt.Sprintf("bench-room-%d", idx)

			err := engine.Subscribe(context.Background(), chName, func(channel string, payload []byte) {
				atomic.AddInt64(&msgCounter, 1)
			})

			if err != nil {
				log.Printf("Subscribe failed: %v", err)
			}
		}(i)
	}

	wg.Wait()
	log.Printf("Subscribed to %d channels successfully.", numChannels)

	// Simulate high throughput fan-out publishing
	go func() {
		for {
			chIdx := rand.Intn(numChannels) // Best effort spreading
			chName := fmt.Sprintf("bench-room-%d", chIdx)
			engine.Publish(context.Background(), chName, []byte(`{"test":"benchmark"}`))
			time.Sleep(1 * time.Microsecond) // Publish fast
		}
	}()

	// Read output stats constantly
	ticker := time.NewTicker(2 * time.Second)
	var lastCount int64

	for range ticker.C {
		current := atomic.LoadInt64(&msgCounter)
		perSec := (current - lastCount) / 2
		lastCount = current
		log.Printf("[Benchmark Status] Processed: %d msgs/sec", perSec)
	}
}
