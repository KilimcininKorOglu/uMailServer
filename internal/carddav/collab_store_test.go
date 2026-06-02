package carddav

import (
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestCollabStoreCrossProtocol verifies the semcore-backed contacts Store writes
// contacts into the same contacts folder EWS reads from, so a contact created
// through the webmail/CardDAV surface is visible over EWS (one source of truth),
// and that upsert-by-UID keeps a single record across edits.
func TestCollabStoreCrossProtocol(t *testing.T) {
	store, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})

	cs := NewCollabStore(store.Collaboration(), store.Identity())
	user := "alice@ex.test"

	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c-1\r\nFN:Bob Jones\r\nEMAIL:bob@ex.test\r\nEND:VCARD"
	contact := &Contact{UID: "c-1", FullName: "Bob Jones"}
	if err := cs.SaveContact(user, defaultAddressbookID, contact, vcf); err != nil {
		t.Fatalf("SaveContact: %v", err)
	}

	got, err := cs.GetContact(user, defaultAddressbookID, "c-1")
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if !strings.Contains(got, "UID:c-1") {
		t.Errorf("GetContact missing contact, got: %q", got)
	}
	contacts, err := cs.GetContacts(user, defaultAddressbookID)
	if err != nil {
		t.Fatalf("GetContacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(contacts))
	}

	// Cross-protocol: the contact lands in the folder EWS reads, so the EWS read
	// path (ListContactsByFolder) sees the webmail-created contact.
	folderID, err := store.Identity().GetFolderID(user, "contacts")
	if err != nil {
		t.Fatalf("GetFolderID: %v", err)
	}
	items, err := store.Collaboration().ListContactsByFolder(folderID)
	if err != nil {
		t.Fatalf("ListContactsByFolder: %v", err)
	}
	if len(items) != 1 || items[0].IcalUID != "c-1" {
		t.Fatalf("EWS read path does not see the webmail-created contact: %+v", items)
	}

	// Upsert by UID keeps exactly one record.
	vcf2 := strings.Replace(vcf, "Bob Jones", "Bob J. Jones", 1)
	contact.FullName = "Bob J. Jones"
	if err := cs.SaveContact(user, defaultAddressbookID, contact, vcf2); err != nil {
		t.Fatalf("SaveContact update: %v", err)
	}
	contacts, err = cs.GetContacts(user, defaultAddressbookID)
	if err != nil {
		t.Fatalf("GetContacts after update: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("expected 1 contact after upsert, got %d", len(contacts))
	}
	if !strings.Contains(contacts[0], "Bob J. Jones") {
		t.Errorf("upsert did not update the record: %q", contacts[0])
	}

	// Delete by UID.
	if err := cs.DeleteContact(user, defaultAddressbookID, "c-1"); err != nil {
		t.Fatalf("DeleteContact: %v", err)
	}
	contacts, err = cs.GetContacts(user, defaultAddressbookID)
	if err != nil {
		t.Fatalf("GetContacts after delete: %v", err)
	}
	if len(contacts) != 0 {
		t.Errorf("expected 0 contacts after delete, got %d", len(contacts))
	}
}
