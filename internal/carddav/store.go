package carddav

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
