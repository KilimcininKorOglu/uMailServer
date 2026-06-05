package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CounterStore provides atomic, TTL-bounded counters and lockout flags backed
// by Redis so rate-limit and lockout state is shared across cluster nodes
// behind a round-robin load balancer. Keys are passed verbatim; the caller
// namespaces them. All operations are best-effort from the caller's view: on a
// Redis error the caller falls back to local behavior rather than denying
// service, since a coordination-layer blip must not lock every node's users out.
type CounterStore interface {
	// IncrSliding increments key and (re)sets its TTL to window on every call,
	// so the window slides forward from the most recent increment. Returns the
	// new counter value.
	IncrSliding(ctx context.Context, key string, window time.Duration) (int64, error)
	// IncrFixed increments key and sets its TTL to window only on the first
	// increment, so the window stays fixed from the first event until it
	// expires. Returns the new counter value.
	IncrFixed(ctx context.Context, key string, window time.Duration) (int64, error)
	// Count returns the current counter value, or 0 if the key is absent.
	Count(ctx context.Context, key string) (int64, error)
	// SetFlag sets a presence flag at key that expires after ttl.
	SetFlag(ctx context.Context, key string, ttl time.Duration) error
	// HasFlag reports whether the flag key is currently present.
	HasFlag(ctx context.Context, key string) (bool, error)
	// Del removes the given keys.
	Del(ctx context.Context, keys ...string) error
	// Close releases the underlying connection.
	Close() error
}

// slidingIncrScript increments and always refreshes the TTL (sliding window).
var slidingIncrScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ARGV[1])
return n
`)

// fixedIncrScript increments and sets the TTL only on the first increment so the
// window is fixed from the first event (matches a fixed-window rate limiter).
var fixedIncrScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return n
`)

// RedisCounterStore implements CounterStore over a Redis connection.
type RedisCounterStore struct {
	client *redis.Client
}

// NewRedisCounterStore creates a Redis-backed counter store.
func NewRedisCounterStore(redisURL string) (*RedisCounterStore, error) {
	client, err := newRedisClient(redisURL)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &RedisCounterStore{client: client}, nil
}

// IncrSliding increments key and refreshes its TTL to window on every call.
func (r *RedisCounterStore) IncrSliding(ctx context.Context, key string, window time.Duration) (int64, error) {
	return slidingIncrScript.Run(ctx, r.client, []string{key}, window.Milliseconds()).Int64()
}

// IncrFixed increments key and sets its TTL to window only on the first call.
func (r *RedisCounterStore) IncrFixed(ctx context.Context, key string, window time.Duration) (int64, error) {
	return fixedIncrScript.Run(ctx, r.client, []string{key}, window.Milliseconds()).Int64()
}

// Count returns the current counter value, treating an absent key as 0.
func (r *RedisCounterStore) Count(ctx context.Context, key string) (int64, error) {
	v, err := r.client.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return v, err
}

// SetFlag sets a presence flag at key that expires after ttl.
func (r *RedisCounterStore) SetFlag(ctx context.Context, key string, ttl time.Duration) error {
	return r.client.Set(ctx, key, 1, ttl).Err()
}

// HasFlag reports whether the flag key is currently present.
func (r *RedisCounterStore) HasFlag(ctx context.Context, key string) (bool, error) {
	n, err := r.client.Exists(ctx, key).Result()
	return n > 0, err
}

// Del removes the given keys.
func (r *RedisCounterStore) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return r.client.Del(ctx, keys...).Err()
}

// Close releases the underlying Redis connection.
func (r *RedisCounterStore) Close() error {
	return r.client.Close()
}
