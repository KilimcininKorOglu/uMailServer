package tls

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeLocker is a minimal in-process mutex used to exercise certmagicStorage's
// Lock/Unlock delegation without a real db-backed locker.
type fakeLocker struct {
	mu   sync.Mutex
	held bool
}

func (l *fakeLocker) Lock(ctx context.Context, _ string) error {
	for {
		l.mu.Lock()
		if !l.held {
			l.held = true
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func (l *fakeLocker) Unlock(_ context.Context, _ string) error {
	l.mu.Lock()
	l.held = false
	l.mu.Unlock()
	return nil
}

// TestCertmagicStorage covers the certmagic.Storage contract the adapter
// presents: bytes round-trip, an absent key reads as fs.ErrNotExist on both Load
// and Stat (so certmagic treats it as a cache miss, not a fatal fault), List
// returns the sorted prefix subtree, Exists is best-effort, and Lock is mutually
// exclusive (a second acquire blocks then fails on ctx — the guarantee that two
// nodes sharing storage never double-issue).
func TestCertmagicStorage(t *testing.T) {
	store := &fakeCacheStore{m: map[string][]byte{}}
	cs := newCertmagicStorage(store, &fakeLocker{})
	ctx := context.Background()

	if err := cs.Store(ctx, "certificates/acme/a.crt", []byte("PEM")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := cs.Load(ctx, "certificates/acme/a.crt")
	if err != nil || string(got) != "PEM" {
		t.Fatalf("Load = %q,%v, want PEM,nil", got, err)
	}
	// Absent key must surface as fs.ErrNotExist, never as a raw error.
	if _, err := cs.Load(ctx, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load(absent) = %v, want fs.ErrNotExist", err)
	}

	if !cs.Exists(ctx, "certificates/acme/a.crt") {
		t.Fatal("Exists should be true for a stored key")
	}
	if cs.Exists(ctx, "missing") {
		t.Fatal("Exists should be false for an absent key")
	}

	if err := store.Put("certificates/acme/b.crt", []byte("xx")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	keys, err := cs.List(ctx, "certificates/acme/", true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"certificates/acme/a.crt", "certificates/acme/b.crt"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("List = %v, want %v", keys, want)
	}

	ki, err := cs.Stat(ctx, "certificates/acme/a.crt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if ki.Key != "certificates/acme/a.crt" || ki.Size != 3 || !ki.IsTerminal {
		t.Fatalf("Stat = %+v, want Key/Size=3/IsTerminal", ki)
	}
	if _, err := cs.Stat(ctx, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(absent) = %v, want fs.ErrNotExist", err)
	}

	if err := cs.Delete(ctx, "certificates/acme/a.crt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if cs.Exists(ctx, "certificates/acme/a.crt") {
		t.Fatal("Exists should be false after Delete")
	}

	// Lock is mutually exclusive: a holder blocks a second acquire until ctx
	// expires, then the holder's Unlock lets it through.
	if err := cs.Lock(ctx, "issue_cert"); err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	bCtx, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	if err := cs.Lock(bCtx, "issue_cert"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Lock while held = %v, want DeadlineExceeded", err)
	}
	if err := cs.Unlock(ctx, "issue_cert"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := cs.Lock(ctx, "issue_cert"); err != nil {
		t.Fatalf("Lock after Unlock: %v", err)
	}
	if err := cs.Unlock(ctx, "issue_cert"); err != nil {
		t.Fatalf("final Unlock: %v", err)
	}
}
