package semcore

import (
	"testing"
	"time"
)

// boolPtr returns a pointer to b, for building optional SearchFolderDef fields.
func boolPtr(b bool) *bool { return &b }

// ---------------------------------------------------------------------------
// SearchFolderDef.Matches
// ---------------------------------------------------------------------------

func TestSearchFolderDefMatches(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name          string
		def           SearchFolderDef
		from          string
		subject       string
		body          string
		date          time.Time
		hasAttachment bool
		want          bool
	}{
		{
			name: "empty definition matches anything",
			def:  SearchFolderDef{},
			from: "anyone@x.com", subject: "hello", body: "world", date: base,
			want: true,
		},
		{
			name: "from contains case-insensitive",
			def:  SearchFolderDef{From: "BOSS@"},
			from: "boss@corp.com", subject: "Q3", date: base,
			want: true,
		},
		{
			name: "from mismatch excludes",
			def:  SearchFolderDef{From: "boss@"},
			from: "peer@corp.com", date: base,
			want: false,
		},
		{
			name:    "subject contains",
			def:     SearchFolderDef{Subject: "invoice"},
			subject: "Your INVOICE is ready", date: base,
			want: true,
		},
		{
			name: "body contains",
			def:  SearchFolderDef{Body: "wire transfer"},
			body: "Please complete the Wire Transfer today", date: base,
			want: true,
		},
		{
			name: "body criterion fails when body empty",
			def:  SearchFolderDef{Body: "wire transfer"},
			body: "", date: base,
			want: false,
		},
		{
			name: "all text criteria must hold (AND)",
			def:  SearchFolderDef{From: "boss@", Subject: "invoice"},
			from: "boss@corp.com", subject: "lunch", date: base,
			want: false,
		},
		{
			name: "date within inclusive range",
			def:  SearchFolderDef{DateFrom: "2026-05-01", DateTo: "2026-06-30"},
			date: base,
			want: true,
		},
		{
			name: "date before lower bound excludes",
			def:  SearchFolderDef{DateFrom: "2026-06-15"},
			date: base,
			want: false,
		},
		{
			name: "date after upper bound excludes",
			def:  SearchFolderDef{DateTo: "2026-05-31"},
			date: base,
			want: false,
		},
		{
			name: "date-only upper bound includes whole day",
			def:  SearchFolderDef{DateTo: "2026-06-01"},
			date: time.Date(2026, 6, 1, 23, 59, 0, 0, time.UTC),
			want: true,
		},
		{
			name: "bounded range excludes unknown (zero) date",
			def:  SearchFolderDef{DateFrom: "2026-01-01"},
			date: time.Time{},
			want: false,
		},
		{
			name: "unparseable date bound is skipped",
			def:  SearchFolderDef{DateFrom: "not-a-date"},
			from: "x@y.com", date: time.Time{},
			want: true,
		},
		{
			name: "has-attachment true required",
			def:  SearchFolderDef{HasAttachment: boolPtr(true)},
			date: base, hasAttachment: true,
			want: true,
		},
		{
			name: "has-attachment true mismatch",
			def:  SearchFolderDef{HasAttachment: boolPtr(true)},
			date: base, hasAttachment: false,
			want: false,
		},
		{
			name: "has-attachment false required",
			def:  SearchFolderDef{HasAttachment: boolPtr(false)},
			date: base, hasAttachment: false,
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.def.Matches(tc.from, tc.subject, tc.body, tc.date, tc.hasAttachment)
			if got != tc.want {
				t.Fatalf("Matches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSearchFolderDefMatchesNil(t *testing.T) {
	var d *SearchFolderDef
	if d.Matches("a", "b", "c", time.Now(), false) {
		t.Fatal("nil SearchFolderDef must not match")
	}
}

// ---------------------------------------------------------------------------
// SetFolderSearchDefinition + ListSearchFolders round-trip
// ---------------------------------------------------------------------------

func TestSearchFolderDefinitionRoundTrip(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	const mbox = "user@example.com"
	if _, err := store.EnsureMailboxId(mbox); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}

	// A plain user folder and a folder we will promote to a search folder.
	plainID, err := store.EnsureFolderId(mbox, "Projects", "")
	if err != nil {
		t.Fatalf("EnsureFolderId(Projects): %v", err)
	}
	searchID, err := store.EnsureFolderId(mbox, "From Boss", "")
	if err != nil {
		t.Fatalf("EnsureFolderId(From Boss): %v", err)
	}

	def := &SearchFolderDef{
		From:        "boss@corp.com",
		Body:        "urgent",
		BaseFolders: []string{"INBOX"},
		Traversal:   "Deep",
	}
	if err := store.SetFolderSearchDefinition(searchID, def); err != nil {
		t.Fatalf("SetFolderSearchDefinition: %v", err)
	}

	// The plain folder must not be reported as a search folder.
	rec, err := store.GetFolderByID(plainID)
	if err != nil {
		t.Fatalf("GetFolderByID(plain): %v", err)
	}
	if rec.SearchDefinition != nil {
		t.Fatal("plain folder unexpectedly carries a SearchDefinition")
	}

	// The promoted folder round-trips its definition.
	rec, err = store.GetFolderByID(searchID)
	if err != nil {
		t.Fatalf("GetFolderByID(search): %v", err)
	}
	if rec.SearchDefinition == nil {
		t.Fatal("search folder lost its SearchDefinition")
	}
	if rec.SearchDefinition.From != "boss@corp.com" || rec.SearchDefinition.Body != "urgent" {
		t.Fatalf("round-tripped def mismatch: %+v", rec.SearchDefinition)
	}
	if len(rec.SearchDefinition.BaseFolders) != 1 || rec.SearchDefinition.BaseFolders[0] != "INBOX" {
		t.Fatalf("BaseFolders mismatch: %+v", rec.SearchDefinition.BaseFolders)
	}

	// ListSearchFolders returns only the search folder.
	list, err := store.ListSearchFolders(mbox)
	if err != nil {
		t.Fatalf("ListSearchFolders: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListSearchFolders returned %d folders, want 1", len(list))
	}
	if !list[0].FolderID.Equal(searchID) {
		t.Fatalf("ListSearchFolders returned %v, want %v", list[0].FolderID, searchID)
	}

	// Clearing the definition demotes it back to a plain folder.
	if err := store.SetFolderSearchDefinition(searchID, nil); err != nil {
		t.Fatalf("SetFolderSearchDefinition(clear): %v", err)
	}
	list, err = store.ListSearchFolders(mbox)
	if err != nil {
		t.Fatalf("ListSearchFolders after clear: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("ListSearchFolders after clear returned %d, want 0", len(list))
	}
}
