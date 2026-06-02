package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/umailserver/umailserver/internal/caldav"
	"github.com/umailserver/umailserver/internal/carddav"
	"github.com/umailserver/umailserver/internal/semcore"
)

// icalUID extracts the UID property from a raw iCalendar or vCard payload.
func icalUID(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) >= 4 && strings.EqualFold(line[:4], "UID:") {
			return strings.TrimSpace(line[4:])
		}
	}
	return ""
}

// unsanitizeUser reverses caldav/carddav.Storage's username sanitization
// ("@" -> "_at_"), recovering the mailbox email from a storage directory name.
func unsanitizeUser(dir string) string {
	return strings.Replace(dir, "_at_", "@", 1)
}

// migrateDAVToCollab imports every event and contact from the legacy filesystem
// CalDAV/CardDAV stores (DataDir/caldav, DataDir/carddav) into the canonical
// semcore collaboration store, so data created before the store unification
// remains visible across EWS, CalDAV/CardDAV, and webmail. It is idempotent:
// items upsert by UID, so re-running it does not duplicate. The server must be
// stopped (the semcore Bolt database is single-writer).
func migrateDAVToCollab(dataDir string, dryRun bool) error {
	store, err := semcore.NewStore(dataDir)
	if err != nil {
		return fmt.Errorf("open semcore store: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: close semcore store: %v\n", cerr)
		}
	}()

	calStore := caldav.NewCollabStore(store.Collaboration(), store.Identity())
	cardStore := carddav.NewCollabStore(store.Collaboration(), store.Identity())

	// caldav.NewStorage / carddav.NewStorage append their own subdirectory, so
	// the on-disk roots are DataDir/caldav/caldav and DataDir/carddav/carddav.
	calRoot := filepath.Join(dataDir, "caldav", "caldav")
	cardRoot := filepath.Join(dataDir, "carddav", "carddav")

	events, evSkipped, err := migrateDAVTree(calRoot, ".ics", func(user, uid, raw string) error {
		if dryRun {
			return nil
		}
		return calStore.SaveEvent(user, "default", &caldav.CalendarEvent{UID: uid}, raw)
	})
	if err != nil {
		return err
	}
	contacts, ctSkipped, err := migrateDAVTree(cardRoot, ".vcf", func(user, uid, raw string) error {
		if dryRun {
			return nil
		}
		return cardStore.SaveContact(user, "default", &carddav.Contact{UID: uid}, raw)
	})
	if err != nil {
		return err
	}

	mode := ""
	if dryRun {
		mode = " (dry-run, nothing written)"
	}
	fmt.Printf("DAV migration%s: %d events migrated, %d skipped; %d contacts migrated, %d skipped\n",
		mode, events, evSkipped, contacts, ctSkipped)
	return nil
}

// migrateDAVTree walks a filesystem DAV root, deriving the mailbox from the
// top-level directory and the UID from each payload, invoking save for each.
// It returns the migrated and skipped counts. Per-item failures are reported
// and skipped (fail-loud) rather than aborting the whole migration.
func migrateDAVTree(root, ext string, save func(user, uid, raw string) error) (migrated, skipped int, err error) {
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		return 0, 0, nil
	}
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ext) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			skipped++
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) == 0 || parts[0] == "" {
			skipped++
			return nil
		}
		user := unsanitizeUser(parts[0])
		data, readErr := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- walking a known DAV data root
		if readErr != nil {
			skipped++
			fmt.Fprintf(os.Stderr, "skip %s: read: %v\n", path, readErr)
			return nil
		}
		uid := icalUID(string(data))
		if uid == "" {
			skipped++
			fmt.Fprintf(os.Stderr, "skip %s: no UID\n", path)
			return nil
		}
		if saveErr := save(user, uid, string(data)); saveErr != nil {
			skipped++
			fmt.Fprintf(os.Stderr, "skip %s: save: %v\n", path, saveErr)
			return nil
		}
		migrated++
		return nil
	})
	return migrated, skipped, walkErr
}
