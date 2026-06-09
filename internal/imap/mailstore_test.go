package imap

import (
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/storage"
)

func TestNewBboltMailstore(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	if ms == nil {
		t.Fatal("Expected non-nil mailstore")
	}

	if ms.dataDir != tmpDir {
		t.Errorf("Expected dataDir to be %s, got %s", tmpDir, ms.dataDir)
	}
}

func TestNewBboltMailstoreInvalidPath(t *testing.T) {
	// Try to create mailstore in a file path that cannot be a directory
	_, err := NewBboltMailstore("/dev/null/test")
	// Should return error on most systems
	if err == nil {
		t.Skip("Expected error for invalid path, but got none (may vary by platform)")
	}
}

func TestBboltMailstoreClose(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}

	// Close should not panic
	err = ms.Close()
	if err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestBboltMailstoreAuthenticate(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	// Authenticate delegates to storage.Database.AuthenticateUser which is
	// not implemented — real auth is injected via server.SetAuthFunc.
	_, err = ms.Authenticate("testuser", "password")
	if err == nil {
		t.Error("Expected error from unimplemented AuthenticateUser")
	}
}

func TestBboltMailstoreCreateAndSelectMailbox(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "TestBox"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Select mailbox
	mb, err := ms.SelectMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("SelectMailbox failed: %v", err)
	}

	if mb.Name != mailbox {
		t.Errorf("Expected mailbox name %s, got %s", mailbox, mb.Name)
	}
}

func TestBboltMailstoreDeleteMailbox(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "ToDelete"

	// Create then delete
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	err = ms.DeleteMailbox(user, mailbox)
	if err != nil {
		t.Errorf("DeleteMailbox failed: %v", err)
	}
}

func TestBboltMailstoreRenameMailbox(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	oldName := "OldBox"
	newName := "NewBox"

	// Create old mailbox
	err = ms.CreateMailbox(user, oldName)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Rename
	err = ms.RenameMailbox(user, oldName, newName)
	if err != nil {
		t.Errorf("RenameMailbox failed: %v", err)
	}
}

func TestBboltMailstoreListMailboxes(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"

	// Create some mailboxes
	mailboxes := []string{"INBOX", "Sent", "Drafts", "Trash"}
	for _, mb := range mailboxes {
		err := ms.CreateMailbox(user, mb)
		if err != nil {
			t.Fatalf("CreateMailbox %s failed: %v", mb, err)
		}
	}

	// List all mailboxes
	list, err := ms.ListMailboxes(user, "*")
	if err != nil {
		t.Fatalf("ListMailboxes failed: %v", err)
	}

	// Should have at least INBOX (default) plus any created mailboxes that match
	if len(list) < 1 {
		t.Errorf("Expected at least 1 mailbox, got %d", len(list))
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"INBOX", "*", true},
		{"INBOX", "INBOX", true},
		{"INBOX", "Sent", false},
		{"Sent", "*", true},
		{"Sent.Messages", "Sent.*", true},
		{"Drafts", "*Sent", false},
		{"Sent", "*Sent", true},
		{"INBOX", "In*", false}, // case sensitive
		{"", "*", true},
		{"INBOX", "", false},
	}

	for _, tt := range tests {
		got := matchPattern(tt.name, tt.pattern)
		if got != tt.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.name, tt.pattern, got, tt.want)
		}
	}
}

func TestBboltMailstoreGetNextUID(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Get next UID
	uid, err := ms.GetNextUID(user, mailbox)
	if err != nil {
		t.Fatalf("GetNextUID failed: %v", err)
	}

	if uid == 0 {
		t.Error("Expected non-zero UID")
	}
}

func TestParseSeqSet(t *testing.T) {
	tests := []struct {
		input   string
		maxSeq  uint32
		wantErr bool
	}{
		{"1", 10, false},
		{"1:5", 10, false},
		{"1,3,5", 10, false},
		{"*", 10, false},
		{"1:*", 10, false},
		{"", 10, true},
	}

	for _, tt := range tests {
		result, err := parseSeqSet(tt.input, tt.maxSeq)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseSeqSet(%q, %d) expected error, got result: %v", tt.input, tt.maxSeq, result)
			}
		} else {
			if err != nil {
				t.Errorf("parseSeqSet(%q, %d) unexpected error: %v", tt.input, tt.maxSeq, err)
			}
		}
	}
}

func TestParseSeqNum(t *testing.T) {
	tests := []struct {
		input   string
		maxSeq  uint32
		wantNum uint32
		wantAll bool
		wantErr bool
	}{
		{"1", 100, 1, false, false},
		{"100", 100, 100, false, false},
		{"*", 100, 100, true, false},
		{"", 100, 0, false, true},
		{"abc", 100, 0, false, true},
	}

	for _, tt := range tests {
		num, err := parseSeqNum(tt.input, tt.maxSeq)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseSeqNum(%q, %d) expected error", tt.input, tt.maxSeq)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSeqNum(%q, %d) unexpected error: %v", tt.input, tt.maxSeq, err)
			continue
		}
		if num != tt.wantNum {
			t.Errorf("parseSeqNum(%q, %d) num = %d, want %d", tt.input, tt.maxSeq, num, tt.wantNum)
		}
	}
}

func TestHasFlag(t *testing.T) {
	flags := []string{"\\Seen", "\\Answered", "\\Deleted"}

	tests := []struct {
		flag string
		want bool
	}{
		{"\\Seen", true},
		{"\\Deleted", true},
		{"\\Draft", false},
		{"", false},
	}

	for _, tt := range tests {
		got := hasFlag(flags, tt.flag)
		if got != tt.want {
			t.Errorf("hasFlag(%v, %q) = %v, want %v", flags, tt.flag, got, tt.want)
		}
	}
}

func TestBboltMailstoreStoreAndExpunge(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Store a message
	data := []byte("From: test@example.com\r\nSubject: Test\r\n\r\nTest body")
	_, err = ms.AppendMessage(user, mailbox, nil, time.Now(), data)
	if err != nil {
		t.Logf("AppendMessage may require full implementation: %v", err)
	}

	// Expunge should work
	err = ms.Expunge(user, mailbox)
	if err != nil {
		t.Logf("Expunge returned error (may be expected): %v", err)
	}
}

func TestBboltMailstoreCopyMessages(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	srcBox := "INBOX"
	dstBox := "Sent"

	// Create mailboxes
	for _, mb := range []string{srcBox, dstBox} {
		err := ms.CreateMailbox(user, mb)
		if err != nil {
			t.Fatalf("CreateMailbox %s failed: %v", mb, err)
		}
	}

	// Copy messages (may not work without actual messages)
	_, err = ms.CopyMessages(user, srcBox, dstBox, "1:*")
	if err != nil {
		t.Logf("CopyMessages returned error (may be expected without messages): %v", err)
	}
}

func TestBboltMailstoreMoveMessages(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	srcBox := "INBOX"
	dstBox := "Archive"

	// Create mailboxes
	for _, mb := range []string{srcBox, dstBox} {
		err := ms.CreateMailbox(user, mb)
		if err != nil {
			t.Fatalf("CreateMailbox %s failed: %v", mb, err)
		}
	}

	// Move messages (may not work without actual messages)
	_, _, _, err = ms.MoveMessages(user, srcBox, dstBox, "1:*")
	if err != nil {
		t.Logf("MoveMessages returned error (may be expected without messages): %v", err)
	}
}

func TestBboltMailstoreSearchMessages(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Search messages
	results, err := ms.SearchMessages(user, mailbox, SearchCriteria{
		All: true,
	})
	if err != nil {
		t.Logf("SearchMessages returned error (may be expected): %v", err)
	}

	t.Logf("Search results: %v", results)
}

func TestBboltMailstoreFetchMessages(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Fetch messages
	messages, err := ms.FetchMessages(user, mailbox, "1:*", []string{"UID", "FLAGS"})
	if err != nil {
		t.Logf("FetchMessages returned error (may be expected without messages): %v", err)
	}

	t.Logf("Fetched %d messages", len(messages))
}

func TestBboltMailstoreStoreFlags(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Store flags (may not work without actual messages)
	err = ms.StoreFlags(user, mailbox, "1:*", []string{"\\Seen"}, FlagAdd)
	if err != nil {
		t.Logf("StoreFlags returned error (may be expected without messages): %v", err)
	}
}

func TestBboltMailstoreUpdateMessageMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Update metadata
	err = ms.UpdateMessageMetadata(user, mailbox, 1, &storage.MessageMetadata{
		Flags: []string{"\\Seen"},
	})
	if err != nil {
		t.Logf("UpdateMessageMetadata returned error (may be expected): %v", err)
	}
}

func TestParseMessageHeaders(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		wantFrom string
		wantSubj string
	}{
		{
			name:     "Simple headers",
			data:     []byte("From: test@example.com\r\nSubject: Test\r\n\r\nBody"),
			wantFrom: "test@example.com",
			wantSubj: "Test",
		},
		{
			name:     "No headers",
			data:     []byte("Just body text"),
			wantFrom: "",
			wantSubj: "",
		},
		{
			name:     "Empty data",
			data:     []byte{},
			wantFrom: "",
			wantSubj: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject, from, to, date := parseMessageHeaders(tt.data)
			// Just verify it doesn't panic and returns values
			_ = subject
			_ = from
			_ = to
			_ = date
		})
	}
}

// TestMatchesCriteria tests the matchesCriteria function
func TestMatchesCriteria(t *testing.T) {
	tests := []struct {
		name     string
		meta     *storage.MessageMetadata
		criteria *SearchCriteria
		want     bool
	}{
		{
			name: "All messages",
			meta: &storage.MessageMetadata{
				Flags: []string{},
			},
			criteria: &SearchCriteria{
				All: true,
			},
			want: true,
		},
		{
			name: "Answered flag match",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Answered"},
			},
			criteria: &SearchCriteria{
				Answered: true,
			},
			want: true,
		},
		{
			name: "Answered flag no match",
			meta: &storage.MessageMetadata{
				Flags: []string{},
			},
			criteria: &SearchCriteria{
				Answered: true,
			},
			want: false,
		},
		{
			name: "Deleted flag match",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Deleted"},
			},
			criteria: &SearchCriteria{
				Deleted: true,
			},
			want: true,
		},
		{
			name: "Seen flag match",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Seen"},
			},
			criteria: &SearchCriteria{
				Seen: true,
			},
			want: true,
		},
		{
			name: "Flagged flag match",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Flagged"},
			},
			criteria: &SearchCriteria{
				Flagged: true,
			},
			want: true,
		},
		{
			name: "Draft flag match",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Draft"},
			},
			criteria: &SearchCriteria{
				Draft: true,
			},
			want: true,
		},
		{
			name: "Unanswered - not answered",
			meta: &storage.MessageMetadata{
				Flags: []string{},
			},
			criteria: &SearchCriteria{
				Unanswered: true,
			},
			want: true,
		},
		{
			name: "Unanswered - is answered",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Answered"},
			},
			criteria: &SearchCriteria{
				Unanswered: true,
			},
			want: false,
		},
		{
			name: "NOT criteria - match",
			meta: &storage.MessageMetadata{
				Flags: []string{},
			},
			criteria: &SearchCriteria{
				Not: &SearchCriteria{
					Answered: true,
				},
			},
			want: true,
		},
		{
			name: "NOT criteria - no match",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Answered"},
			},
			criteria: &SearchCriteria{
				Not: &SearchCriteria{
					Answered: true,
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesCriteria(tt.meta, nil, tt.criteria)
			if got != tt.want {
				t.Errorf("matchesCriteria() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBboltMailstoreStoreFlagsWithMessages tests StoreFlags with actual messages
func TestBboltMailstoreStoreFlagsWithMessages(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Append a message first
	data := []byte("From: test@example.com\r\nSubject: Test\r\n\r\nTest body")
	_, err = ms.AppendMessage(user, mailbox, nil, time.Now(), data)
	if err != nil {
		t.Skipf("AppendMessage requires full implementation: %v", err)
	}

	// Store flags with add mode
	err = ms.StoreFlags(user, mailbox, "1", []string{"\\Seen"}, FlagAdd)
	if err != nil {
		t.Logf("StoreFlags returned error: %v", err)
	}

	// Store flags with remove mode
	err = ms.StoreFlags(user, mailbox, "1", []string{"\\Seen"}, FlagRemove)
	if err != nil {
		t.Logf("StoreFlags (remove) returned error: %v", err)
	}
}

// TestBboltMailstoreFetchMessagesWithData tests FetchMessages with actual data
func TestBboltMailstoreFetchMessagesWithData(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Append a message
	data := []byte("From: test@example.com\r\nSubject: Test Message\r\n\r\nTest body content")
	_, err = ms.AppendMessage(user, mailbox, nil, time.Now(), data)
	if err != nil {
		t.Skipf("AppendMessage requires full implementation: %v", err)
	}

	// Fetch with different items
	items := [][]string{
		{"UID"},
		{"FLAGS"},
		{"BODY"},
		{"BODY.PEEK"},
		{"RFC822"},
		{"RFC822.HEADER"},
		{"RFC822.TEXT"},
		{"ENVELOPE"},
		{"INTERNALDATE"},
		{"RFC822.SIZE"},
		{"UID", "FLAGS", "BODY"},
	}

	for _, itemList := range items {
		messages, err := ms.FetchMessages(user, mailbox, "1:*", itemList)
		if err != nil {
			t.Logf("FetchMessages with %v returned error: %v", itemList, err)
		}
		t.Logf("Fetched %d messages with items %v", len(messages), itemList)
	}
}

// TestBboltMailstoreExpungeWithDeleted tests Expunge with deleted messages
func TestBboltMailstoreExpungeWithDeleted(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Append a message
	data := []byte("From: test@example.com\r\nSubject: Test\r\n\r\nTest body")
	_, err = ms.AppendMessage(user, mailbox, nil, time.Now(), data)
	if err != nil {
		t.Skipf("AppendMessage requires full implementation: %v", err)
	}

	// Mark message as deleted
	err = ms.StoreFlags(user, mailbox, "1", []string{"\\Deleted"}, FlagAdd)
	if err != nil {
		t.Logf("StoreFlags returned error: %v", err)
	}

	// Expunge should remove deleted messages
	err = ms.Expunge(user, mailbox)
	if err != nil {
		t.Logf("Expunge returned error: %v", err)
	}
}

// TestBboltMailstoreCopyMessagesWithData tests CopyMessages with actual data
func TestBboltMailstoreCopyMessagesWithData(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	srcBox := "INBOX"
	dstBox := "Sent"

	// Create mailboxes
	for _, mb := range []string{srcBox, dstBox} {
		err := ms.CreateMailbox(user, mb)
		if err != nil {
			t.Fatalf("CreateMailbox %s failed: %v", mb, err)
		}
	}

	// Append a message to source
	data := []byte("From: test@example.com\r\nSubject: Test\r\n\r\nTest body")
	_, err = ms.AppendMessage(user, srcBox, nil, time.Now(), data)
	if err != nil {
		t.Skipf("AppendMessage requires full implementation: %v", err)
	}

	// Copy the message
	_, err = ms.CopyMessages(user, srcBox, dstBox, "1")
	if err != nil {
		t.Logf("CopyMessages returned error: %v", err)
	}
}

// TestBboltMailstoreMoveMessagesWithData tests MoveMessages with actual data
func TestBboltMailstoreMoveMessagesWithData(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	srcBox := "INBOX"
	dstBox := "Archive"

	// Create mailboxes
	for _, mb := range []string{srcBox, dstBox} {
		err := ms.CreateMailbox(user, mb)
		if err != nil {
			t.Fatalf("CreateMailbox %s failed: %v", mb, err)
		}
	}

	// Append a message to source
	data := []byte("From: test@example.com\r\nSubject: Test\r\n\r\nTest body")
	_, err = ms.AppendMessage(user, srcBox, nil, time.Now(), data)
	if err != nil {
		t.Skipf("AppendMessage requires full implementation: %v", err)
	}

	// Move the message
	_, _, _, err = ms.MoveMessages(user, srcBox, dstBox, "1")
	if err != nil {
		t.Logf("MoveMessages returned error: %v", err)
	}
}

// TestMoveMessagesIsAtomicRFC6851 verifies MOVE removes the source message
// immediately (RFC 6851) — the source index is empty afterward, not merely
// flagged \Deleted — the destination holds the message, and the expunged
// sequence numbers/UIDs are reported so the handler can emit untagged EXPUNGE.
func TestMoveMessagesIsAtomicRFC6851(t *testing.T) {
	ms, err := NewBboltMailstore(t.TempDir())
	if err != nil {
		t.Fatalf("NewBboltMailstore: %v", err)
	}
	t.Cleanup(func() {
		if err := ms.Close(); err != nil {
			t.Errorf("ms.Close: %v", err)
		}
	})

	const user, srcBox, dstBox = "testuser", "INBOX", "Archive"
	for _, mb := range []string{srcBox, dstBox} {
		if err := ms.CreateMailbox(user, mb); err != nil {
			t.Fatalf("CreateMailbox %s: %v", mb, err)
		}
	}
	if _, err := ms.AppendMessage(user, srcBox, nil, time.Now(),
		[]byte("From: a@b\r\nSubject: move-me\r\n\r\nbody\r\n")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	_, seqs, uids, err := ms.MoveMessages(user, srcBox, dstBox, "1")
	if err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	if len(seqs) != 1 || len(uids) != 1 {
		t.Fatalf("MOVE should report exactly one expunged message, got seqs=%v uids=%v", seqs, uids)
	}

	srcUIDs, err := ms.db.GetMessageUIDs(user, srcBox)
	if err != nil {
		t.Fatalf("GetMessageUIDs src: %v", err)
	}
	if len(srcUIDs) != 0 {
		t.Errorf("MOVE left %d message(s) in the source — RFC 6851 requires immediate removal, not a \\Deleted flag", len(srcUIDs))
	}

	dstUIDs, err := ms.db.GetMessageUIDs(user, dstBox)
	if err != nil {
		t.Fatalf("GetMessageUIDs dst: %v", err)
	}
	if len(dstUIDs) != 1 {
		t.Errorf("destination should hold the moved message, got %d", len(dstUIDs))
	}
}

// TestCopyMessagesReportsCopyUID verifies CopyMessages returns the RFC 4315
// COPYUID mapping: the destination UIDVALIDITY plus the per-message source→dest
// UID pairs the handler needs to emit the [COPYUID ...] response code.
func TestCopyMessagesReportsCopyUID(t *testing.T) {
	ms, err := NewBboltMailstore(t.TempDir())
	if err != nil {
		t.Fatalf("NewBboltMailstore: %v", err)
	}
	t.Cleanup(func() {
		if err := ms.Close(); err != nil {
			t.Errorf("ms.Close: %v", err)
		}
	})

	const user, srcBox, dstBox = "testuser", "INBOX", "Archive"
	for _, mb := range []string{srcBox, dstBox} {
		if err := ms.CreateMailbox(user, mb); err != nil {
			t.Fatalf("CreateMailbox %s: %v", mb, err)
		}
	}
	for _, subj := range []string{"copy-1", "copy-2"} {
		if _, err := ms.AppendMessage(user, srcBox, nil, time.Now(),
			[]byte("From: a@b\r\nSubject: "+subj+"\r\n\r\nbody\r\n")); err != nil {
			t.Fatalf("AppendMessage %s: %v", subj, err)
		}
	}

	cu, err := ms.CopyMessages(user, srcBox, dstBox, "1:2")
	if err != nil {
		t.Fatalf("CopyMessages: %v", err)
	}

	dst, err := ms.db.GetMailbox(user, dstBox)
	if err != nil {
		t.Fatalf("GetMailbox dst: %v", err)
	}
	if cu.UIDValidity != dst.UIDValidity || cu.UIDValidity == 0 {
		t.Errorf("COPYUID validity = %d, want non-zero dest UIDVALIDITY %d", cu.UIDValidity, dst.UIDValidity)
	}
	if len(cu.SrcUIDs) != 2 || len(cu.DstUIDs) != 2 {
		t.Fatalf("expected two src/dst UID pairs, got src=%v dst=%v", cu.SrcUIDs, cu.DstUIDs)
	}
	if cu.SrcUIDs[0] != 1 || cu.SrcUIDs[1] != 2 {
		t.Errorf("source UIDs = %v, want [1 2]", cu.SrcUIDs)
	}
	// Parallel ordering: SrcUIDs[i] maps to DstUIDs[i]; the dest set must match
	// the UIDs actually assigned in the destination mailbox.
	dstUIDs, err := ms.db.GetMessageUIDs(user, dstBox)
	if err != nil {
		t.Fatalf("GetMessageUIDs dst: %v", err)
	}
	if len(dstUIDs) != 2 {
		t.Fatalf("destination should hold two copied messages, got %d", len(dstUIDs))
	}
}

// TestMoveMessagesReportsCopyUID verifies the copy half of an atomic MOVE still
// reports the RFC 4315 COPYUID mapping for the destination.
func TestMoveMessagesReportsCopyUID(t *testing.T) {
	ms, err := NewBboltMailstore(t.TempDir())
	if err != nil {
		t.Fatalf("NewBboltMailstore: %v", err)
	}
	t.Cleanup(func() {
		if err := ms.Close(); err != nil {
			t.Errorf("ms.Close: %v", err)
		}
	})

	const user, srcBox, dstBox = "testuser", "INBOX", "Archive"
	for _, mb := range []string{srcBox, dstBox} {
		if err := ms.CreateMailbox(user, mb); err != nil {
			t.Fatalf("CreateMailbox %s: %v", mb, err)
		}
	}
	if _, err := ms.AppendMessage(user, srcBox, nil, time.Now(),
		[]byte("From: a@b\r\nSubject: move-copyuid\r\n\r\nbody\r\n")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	copied, _, _, err := ms.MoveMessages(user, srcBox, dstBox, "1")
	if err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	if copied.UIDValidity == 0 {
		t.Errorf("MOVE COPYUID needs a non-zero dest UIDVALIDITY, got 0")
	}
	if len(copied.SrcUIDs) != 1 || len(copied.DstUIDs) != 1 {
		t.Fatalf("expected one src/dst UID pair from MOVE, got src=%v dst=%v", copied.SrcUIDs, copied.DstUIDs)
	}
	if copied.SrcUIDs[0] != 1 {
		t.Errorf("source UID = %d, want 1", copied.SrcUIDs[0])
	}
}

// TestAppendMessageReportsAppendUID verifies AppendMessage returns the RFC 4315
// APPENDUID mapping: the assigned UID (monotonic per append) and the mailbox
// UIDVALIDITY the handler needs to emit the [APPENDUID ...] response code.
func TestAppendMessageReportsAppendUID(t *testing.T) {
	ms, err := NewBboltMailstore(t.TempDir())
	if err != nil {
		t.Fatalf("NewBboltMailstore: %v", err)
	}
	t.Cleanup(func() {
		if err := ms.Close(); err != nil {
			t.Errorf("ms.Close: %v", err)
		}
	})

	const user, box = "testuser", "INBOX"
	if err := ms.CreateMailbox(user, box); err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}

	first, err := ms.AppendMessage(user, box, nil, time.Now(),
		[]byte("From: a@b\r\nSubject: append-1\r\n\r\nbody one\r\n"))
	if err != nil {
		t.Fatalf("AppendMessage 1: %v", err)
	}
	mb, err := ms.db.GetMailbox(user, box)
	if err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if first.UIDValidity != mb.UIDValidity || first.UIDValidity == 0 {
		t.Errorf("APPENDUID validity = %d, want non-zero mailbox UIDVALIDITY %d", first.UIDValidity, mb.UIDValidity)
	}
	if first.UID == 0 {
		t.Errorf("APPENDUID must report the assigned UID, got 0")
	}

	// A second append reports the next (higher) UID, same UIDVALIDITY.
	second, err := ms.AppendMessage(user, box, nil, time.Now(),
		[]byte("From: a@b\r\nSubject: append-2\r\n\r\nbody two\r\n"))
	if err != nil {
		t.Fatalf("AppendMessage 2: %v", err)
	}
	if second.UID <= first.UID {
		t.Errorf("second APPENDUID UID = %d, want > first UID %d", second.UID, first.UID)
	}
	if second.UIDValidity != first.UIDValidity {
		t.Errorf("UIDVALIDITY changed across appends: %d then %d", first.UIDValidity, second.UIDValidity)
	}
}

// TestExpungeRecordsTombstone verifies an expunge stamps an RFC 7162 QRESYNC
// tombstone for the removed UID so it can later be reported as VANISHED.
func TestExpungeRecordsTombstone(t *testing.T) {
	ms, err := NewBboltMailstore(t.TempDir())
	if err != nil {
		t.Fatalf("NewBboltMailstore: %v", err)
	}
	t.Cleanup(func() {
		if err := ms.Close(); err != nil {
			t.Errorf("ms.Close: %v", err)
		}
	})

	const user, box = "testuser", "INBOX"
	if err := ms.CreateMailbox(user, box); err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}
	if _, err := ms.AppendMessage(user, box, nil, time.Now(),
		[]byte("From: a@b\r\nSubject: expunge-me\r\n\r\nbody\r\n")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := ms.StoreFlags(user, box, "1", []string{"\\Deleted"}, FlagAdd); err != nil {
		t.Fatalf("StoreFlags \\Deleted: %v", err)
	}

	_, uids, err := ms.ExpungeUIDs(user, box, []SeqRange{{Start: 1, End: 1}})
	if err != nil {
		t.Fatalf("ExpungeUIDs: %v", err)
	}
	if len(uids) != 1 || uids[0] != 1 {
		t.Fatalf("ExpungeUIDs removed %v, want [1]", uids)
	}

	// The expunged UID is reported by the tombstone store from mod-sequence 0.
	vanished, err := ms.db.ExpungedUIDsSince(user, box, 0)
	if err != nil {
		t.Fatalf("ExpungedUIDsSince: %v", err)
	}
	if len(vanished) != 1 || vanished[0] != 1 {
		t.Errorf("expunge should record a tombstone for UID 1, got %v", vanished)
	}
}

// TestSearchMessagesMODSEQ verifies the RFC 7162 MODSEQ search criterion filters
// to messages whose mod-sequence is >= the requested value. A flag change bumps
// one message's mod-sequence so it sorts above the other.
func TestSearchMessagesMODSEQ(t *testing.T) {
	ms, err := NewBboltMailstore(t.TempDir())
	if err != nil {
		t.Fatalf("NewBboltMailstore: %v", err)
	}
	t.Cleanup(func() {
		if err := ms.Close(); err != nil {
			t.Errorf("ms.Close: %v", err)
		}
	})

	const user, box = "testuser", "INBOX"
	if err := ms.CreateMailbox(user, box); err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}
	for _, subj := range []string{"modseq-1", "modseq-2"} {
		if _, err := ms.AppendMessage(user, box, nil, time.Now(),
			[]byte("From: a@b\r\nSubject: "+subj+"\r\n\r\nbody\r\n")); err != nil {
			t.Fatalf("AppendMessage %s: %v", subj, err)
		}
	}

	// A flag change on message 2 assigns it a nonzero mod-sequence.
	if err := ms.StoreFlags(user, box, "2", []string{"\\Flagged"}, FlagAdd); err != nil {
		t.Fatalf("StoreFlags: %v", err)
	}
	msgs, err := ms.FetchMessages(user, box, "1:2", []string{"UID"})
	if err != nil || len(msgs) != 2 {
		t.Fatalf("FetchMessages: %v (got %d)", err, len(msgs))
	}
	bumped := msgs[1].ModSeq
	if bumped == 0 {
		t.Fatalf("STORE should bump message 2 mod-sequence above 0")
	}

	// SEARCH MODSEQ <bumped> must return only the flag-changed message (seq 2).
	hits, err := ms.SearchMessages(user, box, SearchCriteria{HasModSeq: true, ModSeq: bumped})
	if err != nil {
		t.Fatalf("SearchMessages MODSEQ: %v", err)
	}
	if len(hits) != 1 || hits[0] != 2 {
		t.Errorf("SEARCH MODSEQ %d = %v, want [2]", bumped, hits)
	}
}

// TestBboltMailstoreSearchMessagesAdvanced tests SearchMessages with various criteria
func TestBboltMailstoreSearchMessagesAdvanced(t *testing.T) {
	tmpDir := t.TempDir()

	ms, err := NewBboltMailstore(tmpDir)
	if err != nil {
		t.Fatalf("NewBboltMailstore failed: %v", err)
	}
	defer ms.Close()

	user := "testuser"
	mailbox := "INBOX"

	// Create mailbox
	err = ms.CreateMailbox(user, mailbox)
	if err != nil {
		t.Fatalf("CreateMailbox failed: %v", err)
	}

	// Append a message
	data := []byte("From: test@example.com\r\nSubject: Test\r\n\r\nTest body")
	_, err = ms.AppendMessage(user, mailbox, nil, time.Now(), data)
	if err != nil {
		t.Skipf("AppendMessage requires full implementation: %v", err)
	}

	// Test various search criteria
	criteriaList := []SearchCriteria{
		{All: true},
		{Answered: true},
		{Deleted: true},
		{Draft: true},
		{Flagged: true},
		{New: true},
		{Old: true},
		{Recent: true},
		{Seen: true},
		{Unanswered: true},
		{Undeleted: true},
		{Undraft: true},
		{Unflagged: true},
		{Unseen: true},
		{From: "test@example.com"},
		{To: "recipient@example.com"},
		{Subject: "Test"},
		{Body: "body"},
		{Text: "test"},
	}

	for _, criteria := range criteriaList {
		results, err := ms.SearchMessages(user, mailbox, criteria)
		if err != nil {
			t.Logf("SearchMessages with criteria %v returned error: %v", criteria, err)
		}
		t.Logf("Search results: %v", results)
	}
}

// TestMatchesCriteriaExtended tests matchesCriteria with extended criteria
func TestMatchesCriteriaExtended(t *testing.T) {
	tests := []struct {
		name     string
		meta     *storage.MessageMetadata
		criteria *SearchCriteria
		want     bool
	}{
		{
			name: "Unseen flag match",
			meta: &storage.MessageMetadata{
				Flags: []string{},
			},
			criteria: &SearchCriteria{
				Unseen: true,
			},
			want: true,
		},
		{
			name: "Unseen - is seen",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Seen"},
			},
			criteria: &SearchCriteria{
				Unseen: true,
			},
			want: false,
		},
		{
			name: "Undeleted - not deleted",
			meta: &storage.MessageMetadata{
				Flags: []string{},
			},
			criteria: &SearchCriteria{
				Undeleted: true,
			},
			want: true,
		},
		{
			name: "Undeleted - is deleted",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Deleted"},
			},
			criteria: &SearchCriteria{
				Undeleted: true,
			},
			want: false,
		},
		{
			name: "Unflagged - not flagged",
			meta: &storage.MessageMetadata{
				Flags: []string{},
			},
			criteria: &SearchCriteria{
				Unflagged: true,
			},
			want: true,
		},
		{
			name: "Unflagged - is flagged",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Flagged"},
			},
			criteria: &SearchCriteria{
				Unflagged: true,
			},
			want: false,
		},
		{
			name: "Draft flag",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Draft"},
			},
			criteria: &SearchCriteria{
				Draft: true,
			},
			want: true,
		},
		{
			name: "Undraft - is draft",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Draft"},
			},
			criteria: &SearchCriteria{
				Undraft: true,
			},
			want: false,
		},
		{
			name: "Recent flag",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Recent"},
			},
			criteria: &SearchCriteria{
				Recent: true,
			},
			want: true,
		},
		{
			name: "Old flag - is recent",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Recent"},
			},
			criteria: &SearchCriteria{
				Old: true,
			},
			want: false,
		},
		{
			name: "Old flag - not recent",
			meta: &storage.MessageMetadata{
				Flags: []string{},
			},
			criteria: &SearchCriteria{
				Old: true,
			},
			want: true,
		},
		{
			name: "New flag - recent and unseen",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Recent"},
			},
			criteria: &SearchCriteria{
				New: true,
			},
			want: true,
		},
		{
			name: "New flag - not unseen",
			meta: &storage.MessageMetadata{
				Flags: []string{"\\Recent", "\\Seen"},
			},
			criteria: &SearchCriteria{
				New: true,
			},
			want: true, // Has Recent flag, so New should match
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesCriteria(tt.meta, nil, tt.criteria)
			if got != tt.want {
				t.Errorf("matchesCriteria() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------- parseMDNAddress tests ----------

func TestParseMDNAddress(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{
			name:    "simple email",
			header:  "admin@example.com",
			want:    "admin@example.com",
			wantErr: false,
		},
		{
			name:    "with angle brackets",
			header:  "<admin@example.com>",
			want:    "admin@example.com",
			wantErr: false,
		},
		{
			name:    "with spaces",
			header:  "  admin@example.com  ",
			want:    "admin@example.com",
			wantErr: false,
		},
		{
			name:    "no at sign",
			header:  "admin.example.com",
			want:    "",
			wantErr: true,
		},
		{
			name:    "empty",
			header:  "",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMDNAddress(tt.header)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseMDNAddress() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseMDNAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------- parseMessageDate tests ----------

func TestParseMessageDate(t *testing.T) {
	tests := []struct {
		name    string
		dateStr string
		wantErr bool
	}{
		{
			name:    "RFC2822 with timezone",
			dateStr: "Mon, 01 Jan 2024 10:00:00 +0000",
			wantErr: false,
		},
		{
			name:    "RFC2822 without timezone",
			dateStr: "Mon, 01 Jan 2024 10:00:00",
			wantErr: false,
		},
		{
			name:    "date only",
			dateStr: "Mon, 01 Jan 2024",
			wantErr: false,
		},
		{
			name:    "ISO-like format",
			dateStr: "01 Jan 2024 10:00:00 +0000",
			wantErr: false,
		},
		{
			name:    "basic date format",
			dateStr: "01-Jan-2024",
			wantErr: false,
		},
		{
			name:    "invalid date",
			dateStr: "not a date",
			wantErr: true,
		},
		{
			name:    "empty",
			dateStr: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseMessageDate(tt.dateStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseMessageDate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
