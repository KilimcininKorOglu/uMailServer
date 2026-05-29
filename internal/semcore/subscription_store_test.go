package semcore

import (
	"testing"
	"time"
)

func TestSubscription_IsDrained(t *testing.T) {
	now := time.Now()
	past := now.Add(-1 * time.Hour)

	tests := []struct {
		name     string
		sub      Subscription
		wantDrained bool
	}{
		{
			name:     "not drained when DrainedAt is zero",
			sub:      Subscription{ID: SubscriptionId{ID: "s1"}, DrainedAt: time.Time{}},
			wantDrained: false,
		},
		{
			name:     "drained when DrainedAt is set in the past",
			sub:      Subscription{ID: SubscriptionId{ID: "s2"}, DrainedAt: past},
			wantDrained: true,
		},
		{
			name:     "drained when DrainedAt is set to now",
			sub:      Subscription{ID: SubscriptionId{ID: "s3"}, DrainedAt: now},
			wantDrained: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sub.IsDrained(); got != tt.wantDrained {
				t.Errorf("Subscription.IsDrained() = %v, want %v", got, tt.wantDrained)
			}
		})
	}
}

func TestSubscription_IsExpired(t *testing.T) {
	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	tests := []struct {
		name       string
		sub        Subscription
		wantExpired bool
	}{
		{
			name:       "not expired when ExpiresAt is zero",
			sub:        Subscription{ID: SubscriptionId{ID: "s1"}, ExpiresAt: time.Time{}},
			wantExpired: false,
		},
		{
			name:       "expired when ExpiresAt is in the past",
			sub:        Subscription{ID: SubscriptionId{ID: "s2"}, ExpiresAt: past},
			wantExpired: true,
		},
		{
			name:       "not expired when ExpiresAt is in the future",
			sub:        Subscription{ID: SubscriptionId{ID: "s3"}, ExpiresAt: future},
			wantExpired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sub.IsExpired(); got != tt.wantExpired {
				t.Errorf("Subscription.IsExpired() = %v, want %v", got, tt.wantExpired)
			}
		})
	}
}
