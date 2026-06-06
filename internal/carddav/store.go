package carddav

import "github.com/umailserver/umailserver/internal/semcore"

// Store is the contacts persistence surface used by the CardDAV protocol server
// and the webmail contacts handler. Two implementations exist: the legacy
// filesystem *Storage and the canonical *CollabStore (semcore collaboration
// store). Routing both surfaces through one Store keeps EWS, CardDAV, and the
// webmail contacts list reading and writing a single source of truth.
type Store interface {
	CreateAddressbook(username string, ab *Addressbook) error
	GetAddressbook(username, addressbookID string) (*Addressbook, error)
	GetAddressbooks(username string) ([]*Addressbook, error)
	UpdateAddressbook(username string, ab *Addressbook) error
	DeleteAddressbook(username, addressbookID string) error
	SaveContact(username, addressbookID string, contact *Contact, vcardData string) error
	GetContact(username, addressbookID, contactUID string) (string, error)
	GetContacts(username, addressbookID string) ([]string, error)
	DeleteContact(username, addressbookID, contactUID string) error
	SetETag(username, addressbookID, contactUID, etag string) error
	GetETag(username, addressbookID, contactUID string) string
	GetAddressbookETag(username, addressbookID string) string
}

// compile-time assertion: the filesystem Storage satisfies Store.
var _ Store = (*Storage)(nil)

// collabBackend is the contact-identity surface the semcore-backed CardDAV
// CollabStore needs. *semcore.BoltCollaborationStore satisfies it; holding the
// interface keeps CardDAV free of a concrete semantic-core dependency so a
// relational backend can slot in later.
type collabBackend interface {
	FindContactByUID(folderID semcore.FolderId, icalUID string) (msgKey string, rec *semcore.StoredContactIdentity, found bool, err error)
	ListContactsByFolder(folderID semcore.FolderId) ([]semcore.StoredContactIdentity, error)
	PutContactIdentityUnsafe(msgKey string, rec *semcore.StoredContactIdentity) error
	DeleteContactByUID(folderID semcore.FolderId, icalUID string) error
}

// identityBackend is the folder-identity resolution surface the CardDAV collab
// store needs.
type identityBackend interface {
	EnsureFolderId(mboxKey, folderName, role string) (semcore.FolderId, error)
	GetFolderID(mboxKey, folderName string) (semcore.FolderId, error)
}

var (
	_ collabBackend   = (*semcore.BoltCollaborationStore)(nil)
	_ identityBackend = (*semcore.BoltIdentityStore)(nil)
)
