package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Tracker manages presence per channel in Redis.
//
// Presence is tracked per connection and reported per user. For every
// (channel, user) there is:
//
//	orbit:presence:<channel>                    ZSET  member userID, score = latest connection expiry (Unix seconds)
//	orbit:presence:conns:<channel>:<userID>     ZSET  member connection ID, score = that connection's expiry
//	orbit:presence:meta:<channel>:<userID>      STRING the user's presence metadata JSON
//
// The per-user connection set is the cluster-wide refcount: a user is in a
// channel while at least one unexpired connection entry exists, on any node.
// Join / Leave / Finalize are Lua scripts, so "count went 0 -> 1" and
// "count is 0, remove the user" are atomic across nodes. The scripts also
// PUBLISH the resulting presence event, so events are ordered on the channel
// exactly as the state changes happened (a join on node B can never be
// overtaken by a stale left from node A).
//
// The user-level ZSET keeps Count, GetUsers and the REST API working as
// before. All keys expire after 2×TTL so state from crashed nodes ages out.
type Tracker struct {
	client *redis.Client
}

func NewTracker(client *redis.Client) *Tracker {
	return &Tracker{client: client}
}

func usersKey(channel string) string { return fmt.Sprintf("orbit:presence:%s", channel) }
func connsKey(channel, userID string) string {
	return fmt.Sprintf("orbit:presence:conns:%s:%s", channel, userID)
}
func metaKey(channel, userID string) string {
	return fmt.Sprintf("orbit:presence:meta:%s:%s", channel, userID)
}

// nowSeconds is the score clock: Unix seconds with millisecond precision.
func nowSeconds(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

// JoinResult says which presence event, if any, a Join published.
type JoinResult int

const (
	// JoinNone: the user already had another live connection in the channel.
	JoinNone JoinResult = iota
	// JoinJoined: first connection (0 -> 1) of a user who was not present.
	JoinJoined
	// JoinRejoined: first connection (0 -> 1) of a user still listed as
	// present, i.e. a reconnect inside the leave grace window or TTL.
	JoinRejoined
	// JoinUpdated: not the first connection, but it carried metadata, which
	// replaced the user's metadata (last write wins).
	JoinUpdated
)

// JoinEvents are the pre-encoded envelopes Join publishes for each outcome.
type JoinEvents struct {
	Joined, Rejoined, Updated []byte
}

// luaExtend is shared by the scripts: extend a key's TTL, never shorten it.
const luaExtend = `
local function extend(key, ms)
  local cur = redis.call('PTTL', key)
  if cur < ms then redis.call('PEXPIRE', key, ms) end
end
local function touchUser(users, userID, expiry)
  local s = redis.call('ZSCORE', users, userID)
  if (not s) or tonumber(s) < expiry then redis.call('ZADD', users, expiry, userID) end
end
`

// KEYS: conns, users, meta
// ARGV: connID, userID, now, expiry, keyTTLms, hasMeta, metadata, channel, joined, rejoined, updated
var joinScript = redis.NewScript(luaExtend + `
local conns, users, meta = KEYS[1], KEYS[2], KEYS[3]
local connID, userID = ARGV[1], ARGV[2]
local now, expiry, keyTTL = tonumber(ARGV[3]), tonumber(ARGV[4]), tonumber(ARGV[5])
local hasMeta, channel = ARGV[6] == '1', ARGV[8]

redis.call('ZREMRANGEBYSCORE', conns, '-inf', now)
redis.call('ZREM', conns, connID)
local others = redis.call('ZCARD', conns)
redis.call('ZADD', conns, expiry, connID)
extend(conns, keyTTL)

local s = redis.call('ZSCORE', users, userID)
local wasPresent = s and tonumber(s) > now
touchUser(users, userID, expiry)
extend(users, keyTTL)

if others == 0 then
  -- First live connection: its subscribe metadata (or none) defines the
  -- user's, so the joined/rejoined payload and stored state always match.
  if hasMeta then
    redis.call('SET', meta, ARGV[7], 'PX', keyTTL)
  else
    redis.call('DEL', meta)
  end
  if wasPresent then
    redis.call('PUBLISH', channel, ARGV[10])
    return 2
  end
  redis.call('PUBLISH', channel, ARGV[9])
  return 1
end

if hasMeta then
  redis.call('SET', meta, ARGV[7], 'PX', keyTTL)
  redis.call('PUBLISH', channel, ARGV[11])
  return 3
end
if redis.call('EXISTS', meta) == 1 then extend(meta, keyTTL) end
return 0
`)

// Join registers connID as a live connection of userID in channel and
// publishes the matching event from ev (see JoinResult). metadata nil means
// the subscribe frame carried none.
func (t *Tracker) Join(ctx context.Context, channel, userID, connID string, ttl time.Duration, metadata json.RawMessage, ev JoinEvents) (JoinResult, error) {
	now := time.Now()
	hasMeta := "0"
	if metadata != nil {
		hasMeta = "1"
	}
	res, err := joinScript.Run(ctx, t.client,
		[]string{connsKey(channel, userID), usersKey(channel), metaKey(channel, userID)},
		connID, userID, nowSeconds(now), nowSeconds(now.Add(ttl)), (2 * ttl).Milliseconds(),
		hasMeta, []byte(metadata), channel, ev.Joined, ev.Rejoined, ev.Updated,
	).Int()
	return JoinResult(res), err
}

// KEYS: conns, users, meta
// ARGV: connID, userID, now, expiry, keyTTLms
var heartbeatScript = redis.NewScript(luaExtend + `
local conns, users, meta = KEYS[1], KEYS[2], KEYS[3]
local expiry, keyTTL = tonumber(ARGV[4]), tonumber(ARGV[5])
redis.call('ZADD', conns, expiry, ARGV[1])
extend(conns, keyTTL)
touchUser(users, ARGV[2], expiry)
extend(users, keyTTL)
if redis.call('EXISTS', meta) == 1 then extend(meta, keyTTL) end
return 1
`)

// Heartbeat refreshes connID's entry (and the user's presence and metadata
// expiry) in channel. It re-creates entries lost to a Redis restart silently,
// without publishing an event.
func (t *Tracker) Heartbeat(ctx context.Context, channel, userID, connID string, ttl time.Duration) error {
	now := time.Now()
	return heartbeatScript.Run(ctx, t.client,
		[]string{connsKey(channel, userID), usersKey(channel), metaKey(channel, userID)},
		connID, userID, nowSeconds(now), nowSeconds(now.Add(ttl)), (2 * ttl).Milliseconds(),
	).Err()
}

// KEYS: conns   ARGV: connID, now
var leaveScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', tonumber(ARGV[2]))
return redis.call('ZCARD', KEYS[1])
`)

// Leave removes connID from userID's connections in channel and returns how
// many live connections the user still has there, cluster-wide. It never
// removes the user or publishes: when it returns 0 the caller decides when
// to Finalize (immediately, or after the leave grace period).
func (t *Tracker) Leave(ctx context.Context, channel, userID, connID string) (int64, error) {
	return leaveScript.Run(ctx, t.client, []string{connsKey(channel, userID)}, connID, nowSeconds(time.Now())).Int64()
}

// KEYS: conns, users, meta   ARGV: userID, now, channel, left
var finalizeScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', tonumber(ARGV[2]))
if redis.call('ZCARD', KEYS[1]) > 0 then return 0 end
redis.call('DEL', KEYS[1], KEYS[3])
if redis.call('ZREM', KEYS[2], ARGV[1]) == 1 then
  redis.call('PUBLISH', ARGV[3], ARGV[4])
  return 1
end
return 0
`)

// Finalize removes userID from channel and publishes leftMsg, atomically and
// only if the user has no live connection left on any node. It reports
// whether presence.left was published. Running it more than once, or after
// the user reconnected, is harmless: at most one left per presence episode.
func (t *Tracker) Finalize(ctx context.Context, channel, userID string, leftMsg []byte) (bool, error) {
	n, err := finalizeScript.Run(ctx, t.client,
		[]string{connsKey(channel, userID), usersKey(channel), metaKey(channel, userID)},
		userID, nowSeconds(time.Now()), channel, leftMsg,
	).Int()
	return n == 1, err
}

// Clean removes expired users from the set.
func (t *Tracker) Clean(ctx context.Context, channel string) error {
	now := nowSeconds(time.Now())
	return t.client.ZRemRangeByScore(ctx, usersKey(channel), "-inf", fmt.Sprintf("%f", now)).Err()
}

// Count returns the number of active users (not connections) in the channel.
func (t *Tracker) Count(ctx context.Context, channel string) (int64, error) {
	t.Clean(ctx, channel)
	return t.client.ZCard(ctx, usersKey(channel)).Result()
}

// GetUsers returns the list of active user IDs in the channel.
func (t *Tracker) GetUsers(ctx context.Context, channel string) ([]string, error) {
	t.Clean(ctx, channel)
	return t.client.ZRange(ctx, usersKey(channel), 0, -1).Result()
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
	if len(users) == 0 {
		return entries, nil
	}
	keys := make([]string, len(users))
	for i, u := range users {
		keys[i] = metaKey(channel, u)
	}
	vals, err := t.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, u := range users {
		meta := json.RawMessage(`{}`)
		if s, ok := vals[i].(string); ok && s != "" {
			meta = json.RawMessage(s)
		}
		entries = append(entries, PresenceEntry{User: u, Metadata: meta})
	}
	return entries, nil
}
