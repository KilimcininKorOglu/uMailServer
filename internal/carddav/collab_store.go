package carddav

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/semcore"
)

// defaultAddressbookID and defaultAddressbookName mirror the single addressbook
// EWS and the webmail contacts list already expose. The CollabStore maps every
// CardDAV addressbookID to the mailbox's one contacts folder, so all surfaces
// share it.
const (
	defaultAddressbookID   = "default"
	defaultAddressbookName = "Contacts"
)

// CollabStore is the canonical, semcore-backed implementation of Store. Contacts
// live in the same BoltCollaborationStore folder EWS writes to, so a contact
// created via CardDAV or webmail is visible over EWS and vice versa. Each
// mailbox maps to a single contacts folder (role "contacts").
type CollabStore struct {
	collab   collabBackend
	identity identityBackend
}

// NewCollabStore builds a semcore-backed contacts Store.
func NewCollabStore(collab collabBackend, identity identityBackend) *CollabStore {
	return &CollabStore{collab: collab, identity: identity}
}

// compile-time assertion.
var _ Store = (*CollabStore)(nil)

// contactsFolder resolves (creating if needed) the mailbox's single contacts
// folder. It mirrors the EWS create path (internal/ews/collab.go).
func (c *CollabStore) contactsFolder(username string) (semcore.FolderId, error) {
	if fid, err := c.identity.GetFolderID(username, "contacts"); err == nil && !fid.IsZero() {
		return fid, nil
	}
	return c.identity.EnsureFolderId(username, "contacts", "contacts")
}

func quotedRandomETag() string { return fmt.Sprintf("%q", uuid.New().String()) }

func (c *CollabStore) defaultAddressbook() *Addressbook {
	return &Addressbook{ID: defaultAddressbookID, Name: defaultAddressbookName}
}

// CreateAddressbook ensures the mailbox's contacts folder exists. In the
// single-addressbook model the created addressbook is always the default one.
func (c *CollabStore) CreateAddressbook(username string, ab *Addressbook) error {
	if _, err := c.contactsFolder(username); err != nil {
		return err
	}
	if ab.ID == "" {
		ab.ID = defaultAddressbookID
	}
	return nil
}

// GetAddressbook returns the mailbox's single addressbook.
func (c *CollabStore) GetAddressbook(username, addressbookID string) (*Addressbook, error) {
	if _, err := c.contactsFolder(username); err != nil {
		return nil, err
	}
	return c.defaultAddressbook(), nil
}

// GetAddressbooks lists the mailbox's addressbooks (a single default one).
func (c *CollabStore) GetAddressbooks(username string) ([]*Addressbook, error) {
	if _, err := c.contactsFolder(username); err != nil {
		return nil, err
	}
	return []*Addressbook{c.defaultAddressbook()}, nil
}

// UpdateAddressbook is a no-op in the single-addressbook model.
func (c *CollabStore) UpdateAddressbook(username string, ab *Addressbook) error {
	_, err := c.contactsFolder(username)
	return err
}

// DeleteAddressbook clears every contact in the mailbox's contacts folder.
func (c *CollabStore) DeleteAddressbook(username, addressbookID string) error {
	folder, err := c.contactsFolder(username)
	if err != nil {
		return err
	}
	items, err := c.collab.ListContactsByFolder(folder)
	if err != nil {
		return err
	}
	for _, it := range items {
		if derr := c.collab.DeleteContactByUID(folder, it.IcalUID); derr != nil {
			return derr
		}
	}
	return nil
}

// SaveContact upserts a contact into the canonical store keyed by its vCard UID.
func (c *CollabStore) SaveContact(username, addressbookID string, contact *Contact, vcardData string) error {
	folder, err := c.contactsFolder(username)
	if err != nil {
		return err
	}
	mboxID, err := semcore.NewMailboxId(username)
	if err != nil {
		return err
	}
	ck, err := semcore.NewContactChangeKey(uuid.New().String())
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(vcardData))
	rawHash := fmt.Sprintf("%x", sum)

	msgKey, existing, found, ferr := c.collab.FindContactByUID(folder, contact.UID)
	if ferr != nil {
		return ferr
	}
	var contactID semcore.ContactId
	if found {
		contactID = existing.ID
	} else {
		contactID, err = semcore.NewContactId(uuid.New().String())
		if err != nil {
			return err
		}
		msgKey = fmt.Sprintf("contact:%s:%s", folder.String(), contact.UID)
	}

	rec := semcore.NewStoredContactIdentity(contactID, folder, mboxID, ck, contact.UID, rawHash)
	rec.RawData = vcardData
	rec.ETag = ck.String()
	return c.collab.PutContactIdentityUnsafe(msgKey, rec)
}

// GetContact returns the raw vCard for one contact, or "" when absent.
func (c *CollabStore) GetContact(username, addressbookID, contactUID string) (string, error) {
	folder, err := c.contactsFolder(username)
	if err != nil {
		return "", err
	}
	_, rec, found, ferr := c.collab.FindContactByUID(folder, contactUID)
	if ferr != nil {
		return "", ferr
	}
	if !found || rec == nil {
		return "", nil
	}
	return rec.RawData, nil
}

// GetContacts returns the raw vCard of every contact in the addressbook.
func (c *CollabStore) GetContacts(username, addressbookID string) ([]string, error) {
	folder, err := c.contactsFolder(username)
	if err != nil {
		return nil, err
	}
	items, err := c.collab.ListContactsByFolder(folder)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.RawData != "" {
			out = append(out, it.RawData)
		}
	}
	return out, nil
}

// DeleteContact removes a contact by UID (idempotent).
func (c *CollabStore) DeleteContact(username, addressbookID, contactUID string) error {
	folder, err := c.contactsFolder(username)
	if err != nil {
		return err
	}
	return c.collab.DeleteContactByUID(folder, contactUID)
}

// SetETag is a no-op: the canonical ETag is the ContactChangeKey assigned on
// write, returned by GetETag.
func (c *CollabStore) SetETag(username, addressbookID, contactUID, etag string) error { return nil }

// GetETag returns the contact's ChangeKey-based DAV ETag.
func (c *CollabStore) GetETag(username, addressbookID, contactUID string) string {
	folder, err := c.contactsFolder(username)
	if err != nil {
		return quotedRandomETag()
	}
	_, rec, found, ferr := c.collab.FindContactByUID(folder, contactUID)
	if ferr != nil || !found || rec == nil {
		return quotedRandomETag()
	}
	etag := rec.ETag
	if etag == "" {
		etag = rec.ChangeKey.String()
	}
	return fmt.Sprintf("%q", etag)
}

// GetAddressbookETag returns a collection ETag derived from the contained
// contacts' ETags, so DAV clients detect any change in the addressbook.
func (c *CollabStore) GetAddressbookETag(username, addressbookID string) string {
	folder, err := c.contactsFolder(username)
	if err != nil {
		return quotedRandomETag()
	}
	items, err := c.collab.ListContactsByFolder(folder)
	if err != nil {
		return quotedRandomETag()
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.ETag)
		b.WriteString(it.IcalUID)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%q", fmt.Sprintf("%x", sum))
}
