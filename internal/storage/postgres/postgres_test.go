package postgres

import (
	"context"
	"os"
	"testing"
)

// TestOpenMigrate is an integration test: it connects to UMAIL_TEST_POSTGRES_DSN,
// applies the storage schema, and verifies the metadata/search tables exist. It
// skips when the DSN is unset so the default `make test` run stays green; runs
// that provide a database exercise the real connection and schema (no mocks).
func TestOpenMigrate(t *testing.T) {
	dsn := os.Getenv("UMAIL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UMAIL_TEST_POSTGRES_DSN not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Idempotent: a second apply must succeed too.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (second apply): %v", err)
	}

	for _, table := range []string{"mailboxes", "messages", "threads", "mailbox_acl"} {
		var exists bool
		err := db.Pool().QueryRow(ctx,
			"SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)",
			table,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if !exists {
			t.Errorf("table %q missing after Migrate", table)
		}
	}

	// The generated tsvector + GIN index must exist (search foundation).
	var hasGIN bool
	if err := db.Pool().QueryRow(ctx,
		"SELECT EXISTS (SELECT FROM pg_indexes WHERE indexname='idx_messages_search')",
	).Scan(&hasGIN); err != nil {
		t.Fatalf("check search index: %v", err)
	}
	if !hasGIN {
		t.Error("idx_messages_search (GIN) missing after Migrate")
	}
}
