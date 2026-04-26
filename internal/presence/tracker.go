package presence

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Tracker manages user online presence per channel.
type Tracker struct {
	client *redis.Client
	ttl    time.Duration
}

func NewTracker(client *redis.Client, ttl time.Duration) *Tracker {
	return &Tracker{
		client: client,
		ttl:    ttl,
	}
}

// Heartbeat refreshes a user's presence in a specific channel.
// We use a Redis SET per channel combined with individual EXPIRE keys or EXPIRE on a Hash?
// Standard approach for TTL per set member is using a Redis Sorted Set (ZSET) with the score being the expiry timestamp.
func (t *Tracker) Heartbeat(ctx context.Context, channel, userID string) error {
	key := fmt.Sprintf("orbit:presence:%s", channel)
	expiryTime := float64(time.Now().Add(t.ttl).Unix())

	// ZADD adds the user with the expiry timestamp as the score
	err := t.client.ZAdd(ctx, key, redis.Z{Score: expiryTime, Member: userID}).Err()
	if err != nil {
		return err
	}
	
	// Option ally set a TTL on the whole key to avoid eternal garbage if abandoned
	t.client.Expire(ctx, key, t.ttl * 2)

	return nil
}

// Remove drops a user from a channel's presence list immediately.
func (t *Tracker) Remove(ctx context.Context, channel, userID string) error {
	key := fmt.Sprintf("orbit:presence:%s", channel)
	return t.client.ZRem(ctx, key, userID).Err()
}

// Clean removes expired users from the set.
func (t *Tracker) Clean(ctx context.Context, channel string) error {
	key := fmt.Sprintf("orbit:presence:%s", channel)
	now := float64(time.Now().Unix())
	return t.client.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%f", now)).Err()
}

// Count returns the number of active users in the channel.
func (t *Tracker) Count(ctx context.Context, channel string) (int64, error) {
	// Clean first to get an accurate count
	t.Clean(ctx, channel)
	key := fmt.Sprintf("orbit:presence:%s", channel)
	return t.client.ZCard(ctx, key).Result()
}

// GetUsers returns the list of active user IDs in the channel.
func (t *Tracker) GetUsers(ctx context.Context, channel string) ([]string, error) {
	// Clean first to get an accurate list
	t.Clean(ctx, channel)
	key := fmt.Sprintf("orbit:presence:%s", channel)
	return t.client.ZRange(ctx, key, 0, -1).Result()
}
