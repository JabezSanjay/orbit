package pubsub

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/orbit/orbit/internal/metrics"
	"github.com/redis/go-redis/v9"
)

type dispatchJob struct {
	channel string
	payload []byte
}

type RedisEngine struct {
	client     *redis.Client
	pubsub     *redis.PubSub

	subs       map[string]bool
	handlers   map[string]MessageHandler
	subsMutex  sync.RWMutex

	workers    []chan dispatchJob
	numWorkers int

	wg         sync.WaitGroup // tracks in-flight fanout jobs

	ctx        context.Context
	cancelFunc context.CancelFunc
}

func NewRedisEngine(redisURL string) (*RedisEngine, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}

	client := redis.NewClient(opts)
	ctx, cancel := context.WithCancel(context.Background())

	if err := client.Ping(ctx).Err(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	numWorkers := 100
	if envStr := os.Getenv("ORBIT_FANOUT_WORKERS"); envStr != "" {
		if c, err := strconv.Atoi(envStr); err == nil && c > 0 {
			numWorkers = c
		}
	}

	r := &RedisEngine{
		client:     client,
		subs:       make(map[string]bool),
		handlers:   make(map[string]MessageHandler),
		workers:    make([]chan dispatchJob, numWorkers),
		numWorkers: numWorkers,
		ctx:        ctx,
		cancelFunc: cancel,
	}

	// Initialize single Multiplexed PubSub connection
	r.pubsub = r.client.Subscribe(ctx)

	// Spin up workers
	for i := 0; i < numWorkers; i++ {
		// Bounded queue to avoid unbound RAM explosions
		r.workers[i] = make(chan dispatchJob, 1024)
		go r.runWorker(ctx, r.workers[i])
	}

	// Spin up dispatcher loop
	go r.runDispatcher()

	return r, nil
}

func (r *RedisEngine) Publish(ctx context.Context, channel string, payload []byte) error {
	start := time.Now()
	err := r.client.Publish(ctx, channel, payload).Err()
	if err == nil {
		metrics.PublishLatency.Observe(time.Since(start).Seconds())
	}
	return err
}

func (r *RedisEngine) Subscribe(ctx context.Context, channel string, handler MessageHandler) error {
	r.subsMutex.Lock()
	defer r.subsMutex.Unlock()

	if r.subs[channel] {
		return nil
	}

	r.subs[channel] = true
	r.handlers[channel] = handler
	metrics.ActiveSubscriptions.Inc()

	err := r.pubsub.Subscribe(ctx, channel)
	if err != nil {
		// Handled proactively by dispatcher's auto-reconnect backoff if network partitioned
		log.Printf("WARN: Redis Subscribe on %s failed: %v", channel, err)
	}

	return nil
}

func (r *RedisEngine) Unsubscribe(ctx context.Context, channel string) error {
	r.subsMutex.Lock()
	defer r.subsMutex.Unlock()

	if !r.subs[channel] {
		return nil
	}

	delete(r.subs, channel)
	delete(r.handlers, channel)
	metrics.ActiveSubscriptions.Dec()

	return r.pubsub.Unsubscribe(ctx, channel)
}

func (r *RedisEngine) Close() error {
	r.cancelFunc()

	r.subsMutex.Lock()
	defer r.subsMutex.Unlock()
	if r.pubsub != nil {
		r.pubsub.Close()
	}

	return r.client.Close()
}

func (r *RedisEngine) runDispatcher() {
	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for {
		if r.ctx.Err() != nil {
			return
		}

		r.subsMutex.RLock()
		ps := r.pubsub
		r.subsMutex.RUnlock()

		msg, err := ps.ReceiveMessage(r.ctx)
		if err != nil {
			metrics.ReconnectsTotal.Inc()
			log.Printf("ERR: Redis network partition detected: %v. Initiating resilient reconnect...", err)

			// Exponential backoff + Jitter
			jitter := time.Duration(rand.Int63n(int64(backoff)/2 + 1))
			time.Sleep(backoff + jitter)

			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}

			r.resubscribeAll()
			continue
		}

		// Reset on success
		backoff = 100 * time.Millisecond

		job := dispatchJob{channel: msg.Channel, payload: []byte(msg.Payload)}

		h := fnv.New32a()
		h.Write([]byte(job.channel))
		idx := h.Sum32() % uint32(r.numWorkers)

		// Track the job before enqueuing; Done() is called by the worker (or here on drop).
		r.wg.Add(1)
		select {
		case r.workers[idx] <- job:
			// Job scheduled successfully
		default:
			r.wg.Done()
			metrics.DroppedMessagesTotal.Inc()
			// Sample 1% of drops to avoid flooding standard output I/O
			if rand.Float32() < 0.01 {
				log.Printf("WARN (Sampled): Dropping message locally due to saturated worker pool on channel %s", job.channel)
			}
		}
	}
}

func (r *RedisEngine) resubscribeAll() {
	r.subsMutex.Lock()
	defer r.subsMutex.Unlock()

	if r.pubsub != nil {
		r.pubsub.Close()
	}

	r.pubsub = r.client.Subscribe(r.ctx)

	var channels []string
	for ch := range r.subs {
		channels = append(channels, ch)
	}

	if len(channels) > 0 {
		r.pubsub.Subscribe(r.ctx, channels...)
	}
}

func (r *RedisEngine) runWorker(ctx context.Context, ch chan dispatchJob) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-ch:
			r.subsMutex.RLock()
			handler, exists := r.handlers[job.channel]
			r.subsMutex.RUnlock()

			if exists && handler != nil {
				start := time.Now()
				handler(job.channel, job.payload)
				metrics.FanoutLatency.Observe(time.Since(start).Seconds())
			}
			r.wg.Done()
		}
	}
}

// Drain waits for all in-flight fanout worker jobs to complete, or until ctx is cancelled.
func (r *RedisEngine) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
