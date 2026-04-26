package pubsub

import (
	"context"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

type RedisEngine struct {
	client     *redis.Client
	subs       map[string]*redis.PubSub
	subsMutex  sync.RWMutex
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

	// Test connection
	if err := client.Ping(ctx).Err(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	return &RedisEngine{
		client:     client,
		subs:       make(map[string]*redis.PubSub),
		ctx:        ctx,
		cancelFunc: cancel,
	}, nil
}

func (r *RedisEngine) Publish(ctx context.Context, channel string, payload []byte) error {
	return r.client.Publish(ctx, channel, payload).Err()
}

func (r *RedisEngine) Subscribe(ctx context.Context, channel string, handler MessageHandler) error {
	r.subsMutex.Lock()
	defer r.subsMutex.Unlock()

	if _, exists := r.subs[channel]; exists {
		// Already subscribed in this engine instance
		return nil
	}

	pubsub := r.client.Subscribe(ctx, channel)
	r.subs[channel] = pubsub

	// Start reading loop in a goroutine
	go func() {
		ch := pubsub.Channel()
		for {
			select {
			case <-r.ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				handler(msg.Channel, []byte(msg.Payload))
			}
		}
	}()

	return nil
}

func (r *RedisEngine) Unsubscribe(ctx context.Context, channel string) error {
	r.subsMutex.Lock()
	defer r.subsMutex.Unlock()

	if pubsub, exists := r.subs[channel]; exists {
		err := pubsub.Unsubscribe(ctx, channel)
		pubsub.Close()
		delete(r.subs, channel)
		return err
	}
	return nil
}

func (r *RedisEngine) Close() error {
	r.cancelFunc()
	
	r.subsMutex.Lock()
	defer r.subsMutex.Unlock()
	for _, pubsub := range r.subs {
		pubsub.Close()
	}
	r.subs = nil
	return r.client.Close()
}
