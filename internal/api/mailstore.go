package api

import "github.com/umailserver/umailserver/internal/storage"

// MailStore is the message-metadata, mailbox/ACL, and backup-persistence surface
// the API layer needs from the storage backend. It is a consumer-defined
// interface: the API package declares exactly the methods its handlers call, so
// either the bbolt *storage.Database or the relational *postgres.DB can satisfy
// it. Maildir message bodies are read through msgStore, not this interface.
type MailStore interface {
	// Mailboxes and message metadata.
	CreateMailbox(user, mailbox string) error
	DeleteMailbox(user, mailbox string) error
	RenameMailbox(user, oldName, newName string) error
	GetMailbox(user, mailbox string) (*storage.Mailbox, error)
	ListMailboxes(user string) ([]string, error)
	EnsureDefaultMailboxes(user string) error
	GetMailboxCounts(user, mailbox string) (exists, recent, unseen int, err error)
	GetNextUID(user, mailbox string) (uint32, error)
	GetMessageUIDs(user, mailbox string) ([]uint32, error)
	GetMessageMetadata(user, mailbox string, uid uint32) (*storage.MessageMetadata, error)
	StoreMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error
	UpdateMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error
	DeleteMessage(user, mailbox string, uid uint32) error

	// IMAP ACLs (RFC 4314) backing the sharing API.
	GetACL(owner, mailbox, grantee string) (storage.ACLRights, error)
	SetACL(owner, mailbox, grantee string, rights storage.ACLRights, grantingUser string) error
	DeleteACL(owner, mailbox, grantee string) error
	ListACL(owner, mailbox string) ([]storage.ACLEntry, error)
	ListGranteesMailboxes(owner string) ([]string, error)
	ListMailboxesSharedWith(user string) ([]string, error)

	// Admin backup jobs and manifests.
	CreateBackupJob(job *storage.BackupJob) error
	GetBackupJob(id string) (*storage.BackupJob, error)
	UpdateBackupJob(job *storage.BackupJob) error
	DeleteBackupJob(id string) error
	ListBackupJobs(enabledOnly bool) ([]storage.BackupJob, error)
	CreateBackupManifest(manifest *storage.BackupManifest) error
	GetBackupManifest(id string) (*storage.BackupManifest, error)
	DeleteBackupManifest(id string) error
	ListBackupManifests(target string) ([]storage.BackupManifest, error)
}

// The bbolt store satisfies the API's storage surface. The relational
// *postgres.DB does too; that assertion lives in the server package (the
// composition root that imports both), to keep this package free of a
// db/postgres dependency.
var _ MailStore = (*storage.Database)(nil)
