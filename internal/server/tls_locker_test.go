package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// TestTLSLockerContention verifies the blocking Lock contract that coordinates
// certificate issuance across nodes: while one owner holds the lock, a second
// owner's Lock blocks and fails on ctx cancellation (rather than busy-returning
// and letting both nodes issue the same certificate), and once the holder
// releases, the waiter acquires. If Lock returned nil while another node held
// the lease, two nodes would race an ACME order and burn CA rate limits.
func TestTLSLockerContention(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	}()

	lockerA := newTLSLocker(database, "nodeA")
	lockerB := newTLSLocker(database, "nodeB")
	ctx := context.Background()

	if err := lockerA.Lock(ctx, "cert_x"); err != nil {
		t.Fatalf("lockerA.Lock: %v", err)
	}

	// B blocks, then fails when its ctx deadline elapses — it must NOT acquire
	// while A holds.
	bCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	if err := lockerB.Lock(bCtx, "cert_x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lockerB.Lock while A holds = %v, want DeadlineExceeded", err)
	}

	// A releases; B now acquires promptly.
	if err := lockerA.Unlock(ctx, "cert_x"); err != nil {
		t.Fatalf("lockerA.Unlock: %v", err)
	}
	if err := lockerB.Lock(ctx, "cert_x"); err != nil {
		t.Fatalf("lockerB.Lock after release: %v", err)
	}
	if err := lockerB.Unlock(ctx, "cert_x"); err != nil {
		t.Fatalf("lockerB.Unlock: %v", err)
	}
}

// TestTLSLockerSameOwnerReacquire confirms the lease is owner-scoped: two
// lockers on the same node (same owner) both pass Lock because re-acquiring an
// own lease refreshes it rather than self-deadlocking. This is what lets a node
// hold a lock across overlapping operations without blocking itself.
func TestTLSLockerSameOwnerReacquire(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	}()

	lo1 := newTLSLocker(database, "nodeA")
	lo2 := newTLSLocker(database, "nodeA") // same owner
	ctx := context.Background()

	if err := lo1.Lock(ctx, "cert_y"); err != nil {
		t.Fatalf("lo1.Lock: %v", err)
	}
	if err := lo2.Lock(ctx, "cert_y"); err != nil {
		t.Fatalf("lo2.Lock (same owner) should re-acquire the lease, got: %v", err)
	}
	if err := lo1.Unlock(ctx, "cert_y"); err != nil {
		t.Fatalf("lo1.Unlock: %v", err)
	}
}
