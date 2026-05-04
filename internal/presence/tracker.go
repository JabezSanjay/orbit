package presence

import (
	"context"
	"encoding/json"
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
	t.Clean(ctx, channel)
	key := fmt.Sprintf("orbit:presence:%s", channel)
	return t.client.ZCard(ctx, key).Result()
}

// GetUsers returns the list of active user IDs in the channel.
func (t *Tracker) GetUsers(ctx context.Context, channel string) ([]string, error) {
	t.Clean(ctx, channel)
	key := fmt.Sprintf("orbit:presence:%s", channel)
	return t.client.ZRange(ctx, key, 0, -1).Result()
}

// SetMetadata stores opaque JSON metadata for a user in a channel.
// The metadata key uses a 2×TTL expiry, matching the sorted-set key.
func (t *Tracker) SetMetadata(ctx context.Context, channel, userID string, ttl time.Duration, metadata json.RawMessage) error {
	key := fmt.Sprintf("orbit:presence:meta:%s:%s", channel, userID)
	return t.client.Set(ctx, key, []byte(metadata), ttl*2).Err()
}

// RefreshMetadata extends the expiry of a user's metadata key.
func (t *Tracker) RefreshMetadata(ctx context.Context, channel, userID string, ttl time.Duration) {
	key := fmt.Sprintf("orbit:presence:meta:%s:%s", channel, userID)
	t.client.Expire(ctx, key, ttl*2)
}

// RemoveMetadata deletes a user's metadata key for a channel.
func (t *Tracker) RemoveMetadata(ctx context.Context, channel, userID string) {
	key := fmt.Sprintf("orbit:presence:meta:%s:%s", channel, userID)
	t.client.Del(ctx, key)
}

// PresenceEntry holds a user ID and their optional metadata for API responses.
type PresenceEntry struct {
	User     string          `json:"user"`
	Metadata json.RawMessage `json:"metadata"`
}

// GetUsersWithMetadata returns active users paired with their stored metadata.
// Users without metadata get an empty object ({}).
func (t *Tracker) GetUsersWithMetadata(ctx context.Context, channel string) ([]PresenceEntry, error) {
	users, err := t.GetUsers(ctx, channel)
	if err != nil {
		return nil, err
	}
	entries := make([]PresenceEntry, 0, len(users))
	for _, u := range users {
		entries = append(entries, PresenceEntry{User: u, Metadata: t.getMetadata(ctx, channel, u)})
	}
	return entries, nil
}

// getMetadata retrieves stored metadata or returns an empty JSON object.
func (t *Tracker) getMetadata(ctx context.Context, channel, userID string) json.RawMessage {
	key := fmt.Sprintf("orbit:presence:meta:%s:%s", channel, userID)
	val, err := t.client.Get(ctx, key).Bytes()
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return val
}
