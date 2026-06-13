package ews

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestFolderCounts_FromIdentityMatchFindItem verifies that GetFolder's
// TotalCount/UnreadCount are sourced from the identity store — the same source
// FindItem enumerates — so the two surfaces always report the same number for a
// folder. It guards the regression where folderCounts read the storage mailbox
// by display name ("Inbox"), which never matched the canonical "INBOX" storage
// bucket: GetFolder reported TotalCount=0 for a full inbox (Outlook/exchangelib
// saw an empty mailbox) while FindItem returned the real items.
func TestFolderCounts_FromIdentityMatchFindItem(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	const email = "alice@local.test"
	ensureMailboxFixtures(t, srv, email)

	mboxID, err := srv.identity.EnsureMailboxId(email)
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	inboxID, err := srv.identity.EnsureFolderId(email, "inbox", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	// Deliver three messages with DISTINCT content (so the content-addressed
	// identity store keeps all three as separate items): two unread, one read.
	msgs := []struct {
		subject string
		read    bool
	}{
		{"first unread", false},
		{"second unread", false},
		{"already read", true},
	}
	for _, m := range msgs {
		raw := []byte("From: s@x.test\r\nTo: alice@local.test\r\nSubject: " + m.subject +
			"\r\nMessage-ID: <" + m.subject + "@x.test>\r\n\r\nbody of " + m.subject + "\r\n")
		// FindItem reads each item's body from msgStore by its content blob key;
		// MutateItem derives the same key from RawMessage, so storing the body here
		// makes the item renderable (FindItem skips items whose body is absent).
		if _, err := srv.msgStore.StoreMessage(email, raw); err != nil {
			t.Fatalf("StoreMessage %q: %v", m.subject, err)
		}
		in := &semcore.MutationInput{
			MailboxID:    mboxID,
			FolderID:     inboxID,
			RawMessage:   raw,
			InternalDate: time.Unix(1700000000, 0),
			Actor:        email,
			Email:        email,
			Source:       semcore.MutationSourceSMTP,
			IsRead:       m.read,
		}
		if _, err := srv.mutationPipe.MutateItem(in); err != nil {
			t.Fatalf("MutateItem %q: %v", m.subject, err)
		}
	}

	// Add an orphaned identity: register it WITHOUT storing the body, so its blob
	// is absent from msgStore. This models identity-store drift ahead of msgStore
	// (the qa.bob inbox carries such residue). Neither GetFolder's count nor
	// FindItem's enumeration may surface it, so the totals stay 3/2 and equal.
	orphanRaw := []byte("From: s@x.test\r\nTo: alice@local.test\r\nSubject: orphan no body" +
		"\r\nMessage-ID: <orphan-no-body@x.test>\r\n\r\norphan body never stored\r\n")
	if _, err := srv.mutationPipe.MutateItem(&semcore.MutationInput{
		MailboxID:    mboxID,
		FolderID:     inboxID,
		RawMessage:   orphanRaw,
		InternalDate: time.Unix(1700000000, 0),
		Actor:        email,
		Email:        email,
		Source:       semcore.MutationSourceSMTP,
		IsRead:       false,
	}); err != nil {
		t.Fatalf("MutateItem orphan: %v", err)
	}

	// GetFolder reports counts from the readable identity set: 3 total, 2 unread
	// (the orphan is excluded because its body is absent).
	gf := ewsItemRequest(t, srv, email, ewsEnvelope("GetFolder", `
		<FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape>
		<FolderIds><t:DistinguishedFolderId Id="inbox"/></FolderIds>`))
	if gf.Code != http.StatusOK {
		t.Fatalf("GetFolder HTTP status: got %d, want 200", gf.Code)
	}
	gfBody := gf.Body.String()
	if !strings.Contains(gfBody, ">3</TotalCount>") {
		t.Errorf("GetFolder TotalCount: want 3, body:\n%s", gfBody)
	}
	if !strings.Contains(gfBody, ">2</UnreadCount>") {
		t.Errorf("GetFolder UnreadCount: want 2, body:\n%s", gfBody)
	}

	// FindItem must surface the same number of items GetFolder counted: a client
	// that reads TotalCount=3 then enumerates the folder must see exactly 3.
	fi := ewsItemRequest(t, srv, email, ewsEnvelopeWithAttrs("FindItem", ` Traversal="Shallow"`, `
		<ItemShape><t:BaseShape>IdOnly</t:BaseShape></ItemShape>
		<ParentFolderIds><t:DistinguishedFolderId Id="inbox"/></ParentFolderIds>`))
	if fi.Code != http.StatusOK {
		t.Fatalf("FindItem HTTP status: got %d, want 200", fi.Code)
	}
	fiBody := fi.Body.String()
	if !strings.Contains(fiBody, `TotalItemsInView="3"`) {
		t.Errorf("FindItem TotalItemsInView: want 3 (must equal GetFolder TotalCount), body:\n%s", fiBody)
	}
}

// TestFindConversation_ExcludesBodylessOrphan verifies FindConversation applies
// the same readable-body filter as FindItem/GetFolder: a body-less orphan identity
// that shares a conversation must not inflate that conversation's item count, or
// the conversation view would disagree with every other EWS item surface.
func TestFindConversation_ExcludesBodylessOrphan(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	const email = "alice@local.test"
	ensureMailboxFixtures(t, srv, email)
	mboxID, err := srv.identity.EnsureMailboxId(email)
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	inboxID, err := srv.identity.EnsureFolderId(email, "inbox", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	// Two messages in the SAME conversation (the orphan threads onto the stored
	// one via In-Reply-To/References): one has its body stored (renderable), the
	// other is registered without a body (an orphaned identity).
	const subject = "Quarterly report thread"
	stored := []byte("From: s@x.test\r\nTo: alice@local.test\r\nSubject: " + subject +
		"\r\nMessage-ID: <conv-root@x.test>\r\n\r\nthe stored message body\r\n")
	orphan := []byte("From: s@x.test\r\nTo: alice@local.test\r\nSubject: Re: " + subject +
		"\r\nMessage-ID: <conv-orphan@x.test>\r\nIn-Reply-To: <conv-root@x.test>" +
		"\r\nReferences: <conv-root@x.test>\r\n\r\nthis body is never stored\r\n")
	if _, err := srv.msgStore.StoreMessage(email, stored); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}
	for _, raw := range [][]byte{stored, orphan} {
		if _, err := srv.mutationPipe.MutateItem(&semcore.MutationInput{
			MailboxID:    mboxID,
			FolderID:     inboxID,
			RawMessage:   raw,
			InternalDate: time.Unix(1700000000, 0),
			Actor:        email,
			Email:        email,
			Source:       semcore.MutationSourceSMTP,
		}); err != nil {
			t.Fatalf("MutateItem: %v", err)
		}
	}

	rec := ewsItemRequest(t, srv, email, ewsEnvelope("FindConversation", `
		<ItemShape><t:BaseShape>IdOnly</t:BaseShape></ItemShape>
		<ParentFolderIds><t:DistinguishedFolderId Id="inbox"/></ParentFolderIds>`))
	if rec.Code != http.StatusOK {
		t.Fatalf("FindConversation HTTP status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// The conversation holds one renderable message; the body-less orphan must be
	// excluded, so TotalCount is 1, not 2.
	if !strings.Contains(body, ">1</TotalCount>") {
		t.Errorf("conversation TotalCount: want 1 (orphan excluded), body:\n%s", body)
	}
	if strings.Contains(body, ">2</TotalCount>") {
		t.Errorf("conversation TotalCount=2: the body-less orphan was wrongly counted, body:\n%s", body)
	}
}
