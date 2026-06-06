package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core delegate grants. Mirrors *semcore.BoltDelegateStore: a grant is
// keyed by (owner, delegate_email) for upsert and carries a stable "del-..." id.
// The fixed-folder DelegateFolderPermissions struct is flattened to typed
// columns rather than a JSON blob.

const delegateSelectCols = `id, owner_id, delegate_email, delegate_user_id,
	perm_calendar, perm_tasks, perm_inbox, perm_contacts, perm_notes, perm_journal,
	view_private_items, receive_copies, deliver_meeting_requests, can_send_as,
	granted_by, created_at, updated_at`

// PutDelegate upserts a grant keyed by (owner, delegate_email). A new grant gets
// a fresh id and created_at; an existing grant keeps both. Returns the grant id.
func (d *DB) PutDelegate(del *semcore.DelegateUser) (semcore.DelegateId, error) {
	if del == nil {
		return semcore.DelegateId{}, fmt.Errorf("PutDelegate: nil delegate")
	}
	if del.OwnerID.IsZero() {
		return semcore.DelegateId{}, fmt.Errorf("PutDelegate: zero owner ID")
	}
	if del.DelegateEmail == "" {
		return semcore.DelegateId{}, fmt.Errorf("PutDelegate: empty delegate email")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return semcore.DelegateId{}, fmt.Errorf("generate delegate id: %w", err)
	}
	candidate := "del-" + hex.EncodeToString(b)
	p := del.Permissions

	var gotID string
	var createdAt time.Time
	err := d.pool.QueryRow(context.Background(), `
		INSERT INTO semcore_delegate
			(id, owner_id, delegate_email, delegate_user_id,
			 perm_calendar, perm_tasks, perm_inbox, perm_contacts, perm_notes, perm_journal,
			 view_private_items, receive_copies, deliver_meeting_requests, can_send_as,
			 granted_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now(),now())
		ON CONFLICT (owner_id, delegate_email) DO UPDATE SET
			delegate_user_id=EXCLUDED.delegate_user_id,
			perm_calendar=EXCLUDED.perm_calendar, perm_tasks=EXCLUDED.perm_tasks,
			perm_inbox=EXCLUDED.perm_inbox, perm_contacts=EXCLUDED.perm_contacts,
			perm_notes=EXCLUDED.perm_notes, perm_journal=EXCLUDED.perm_journal,
			view_private_items=EXCLUDED.view_private_items, receive_copies=EXCLUDED.receive_copies,
			deliver_meeting_requests=EXCLUDED.deliver_meeting_requests, can_send_as=EXCLUDED.can_send_as,
			granted_by=EXCLUDED.granted_by, updated_at=now()
		RETURNING id, created_at`,
		candidate, del.OwnerID.String(), del.DelegateEmail, del.DelegateUserID,
		string(p.Calendar), string(p.Tasks), string(p.Inbox), string(p.Contacts), string(p.Notes), string(p.Journal),
		del.ViewPrivateItems, del.ReceiveCopies, string(del.DeliverRequests), del.CanSendAs,
		del.GrantedBy,
	).Scan(&gotID, &createdAt)
	if err != nil {
		return semcore.DelegateId{}, fmt.Errorf("postgres: put delegate: %w", err)
	}
	id, err := semcore.NewDelegateId(gotID)
	if err != nil {
		return semcore.DelegateId{}, fmt.Errorf("postgres: put delegate: %w", err)
	}
	del.ID = id
	del.CreatedAt = createdAt
	return id, nil
}

// GetDelegate returns a grant by id, or an error when absent.
func (d *DB) GetDelegate(id semcore.DelegateId) (*semcore.DelegateUser, error) {
	del, err := scanDelegate(d.pool.QueryRow(context.Background(),
		`SELECT `+delegateSelectCols+` FROM semcore_delegate WHERE id=$1`, id.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("delegate not found: %s", id.String())
		}
		return nil, fmt.Errorf("postgres: get delegate %s: %w", id, err)
	}
	return del, nil
}

// GetDelegateForUser returns the grant for (owner, delegate), or an error.
func (d *DB) GetDelegateForUser(ownerID semcore.MailboxId, delegateEmail string) (*semcore.DelegateUser, error) {
	if ownerID.IsZero() || delegateEmail == "" {
		return nil, fmt.Errorf("GetDelegateForUser: invalid args")
	}
	del, err := scanDelegate(d.pool.QueryRow(context.Background(),
		`SELECT `+delegateSelectCols+` FROM semcore_delegate WHERE owner_id=$1 AND delegate_email=$2`,
		ownerID.String(), delegateEmail))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("no delegate grant for %s on %s", delegateEmail, ownerID.String())
		}
		return nil, fmt.Errorf("postgres: get delegate for user: %w", err)
	}
	return del, nil
}

// ListDelegates returns all grants made by an owner.
func (d *DB) ListDelegates(ownerID semcore.MailboxId) ([]*semcore.DelegateUser, error) {
	return d.queryDelegates(`SELECT `+delegateSelectCols+` FROM semcore_delegate WHERE owner_id=$1 ORDER BY delegate_email`,
		ownerID.String())
}

// ListAllDelegates returns every delegate grant.
func (d *DB) ListAllDelegates() ([]*semcore.DelegateUser, error) {
	return d.queryDelegates(`SELECT ` + delegateSelectCols + ` FROM semcore_delegate ORDER BY owner_id, delegate_email`)
}

// RemoveDelegate deletes a grant by id, erroring when absent (bbolt parity).
func (d *DB) RemoveDelegate(id semcore.DelegateId) error {
	if id.IsZero() {
		return fmt.Errorf("RemoveDelegate: zero ID")
	}
	tag, err := d.pool.Exec(context.Background(), `DELETE FROM semcore_delegate WHERE id=$1`, id.String())
	if err != nil {
		return fmt.Errorf("postgres: remove delegate %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delegate not found: %s", id.String())
	}
	return nil
}

func (d *DB) queryDelegates(sql string, args ...any) ([]*semcore.DelegateUser, error) {
	rows, err := d.pool.Query(context.Background(), sql, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list delegates: %w", err)
	}
	defer rows.Close()
	var result []*semcore.DelegateUser
	for rows.Next() {
		del, err := scanDelegate(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, del)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list delegates: %w", err)
	}
	return result, nil
}

func scanDelegate(row rowScanner) (*semcore.DelegateUser, error) {
	var id, ownerID, email, userID string
	var pCal, pTasks, pInbox, pContacts, pNotes, pJournal string
	var viewPriv, recvCopies, canSendAs bool
	var deliver, grantedBy string
	var createdAt, updatedAt time.Time
	if err := row.Scan(&id, &ownerID, &email, &userID,
		&pCal, &pTasks, &pInbox, &pContacts, &pNotes, &pJournal,
		&viewPriv, &recvCopies, &deliver, &canSendAs, &grantedBy, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	did, err := semcore.NewDelegateId(id)
	if err != nil {
		return nil, fmt.Errorf("invalid delegate id %q: %w", id, err)
	}
	return &semcore.DelegateUser{
		ID:             did,
		OwnerID:        parseMailboxID(ownerID),
		DelegateEmail:  email,
		DelegateUserID: userID,
		Permissions: semcore.DelegateFolderPermissions{
			Calendar: semcore.DelegateFolderPermissionLevel(pCal),
			Tasks:    semcore.DelegateFolderPermissionLevel(pTasks),
			Inbox:    semcore.DelegateFolderPermissionLevel(pInbox),
			Contacts: semcore.DelegateFolderPermissionLevel(pContacts),
			Notes:    semcore.DelegateFolderPermissionLevel(pNotes),
			Journal:  semcore.DelegateFolderPermissionLevel(pJournal),
		},
		ViewPrivateItems: viewPriv,
		ReceiveCopies:    recvCopies,
		DeliverRequests:  semcore.DeliverMeetingRequests(deliver),
		CanSendAs:        canSendAs,
		GrantedBy:        grantedBy,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}, nil
}
