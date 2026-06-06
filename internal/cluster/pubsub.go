package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// PubSub is a minimal cross-node fan-out channel backed by Redis pub/sub. It
// carries volatile, at-most-once real-time signals (mailbox change
// notifications for IMAP IDLE and webmail SSE) — never a source of truth — so a
// dropped message degrades to "client learns on its next poll", not data loss.
type PubSub interface {
	// Publish sends payload to all nodes subscribed to channel.
	Publish(ctx context.Context, channel string, payload []byte) error
	// Subscribe returns a stream of payloads published to channel by any node.
	// The returned channel is closed when ctx is canceled or Close is called.
	Subscribe(ctx context.Context, channel string) (<-chan []byte, error)
	// Close releases the underlying connection.
	Close() error
}

// RedisPubSub implements PubSub over a Redis connection.
type RedisPubSub struct {
	client *redis.Client
}

// NewRedisPubSub creates a Redis-backed pub/sub.
func NewRedisPubSub(redisURL string) (*RedisPubSub, error) {
	client, err := newRedisClient(redisURL)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &RedisPubSub{client: client}, nil
}

// Publish sends payload to all subscribers of channel.
func (p *RedisPubSub) Publish(ctx context.Context, channel string, payload []byte) error {
	return p.client.Publish(ctx, channel, payload).Err()
}

// Subscribe returns a stream of payloads for channel. A goroutine forwards
// Redis messages onto the returned channel until ctx is canceled, at which
// point the subscription is closed and the channel drained shut.
func (p *RedisPubSub) Subscribe(ctx context.Context, channel string) (<-chan []byte, error) {
	sub := p.client.Subscribe(ctx, channel)
	// Confirm the subscription is established before returning so callers do not
	// miss messages published immediately after Subscribe returns.
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close() //nolint:errcheck
		return nil, err
	}

	out := make(chan []byte, 256)
	go func() {
		defer close(out)
		defer func() { _ = sub.Close() }() //nolint:errcheck
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- []byte(msg.Payload):
				case <-ctx.Done():
					return
				default:
					// Slow consumer: drop rather than block the Redis reader.
					// These are best-effort wake-ups; the client reconciles on
					// its next poll.
				}
			}
		}
	}()
	return out, nil
}

// Close releases the underlying Redis connection.
func (p *RedisPubSub) Close() error {
	return p.client.Close()
}
