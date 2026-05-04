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
}

func NewTracker(client *redis.Client) *Tracker {
	return &Tracker{client: client}
}

// Heartbeat refreshes a user's presence in a specific channel using the provided TTL.
func (t *Tracker) Heartbeat(ctx context.Context, channel, userID string, ttl time.Duration) error {
	key := fmt.Sprintf("orbit:presence:%s", channel)
	expiryTime := float64(time.Now().Add(ttl).Unix())

	err := t.client.ZAdd(ctx, key, redis.Z{Score: expiryTime, Member: userID}).Err()
	if err != nil {
		return err
	}

	// Set a TTL on the whole key to avoid abandoned keys accumulating.
	t.client.Expire(ctx, key, ttl*2)

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
