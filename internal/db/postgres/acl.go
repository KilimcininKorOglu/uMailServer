package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/storage"
)

// IMAP ACLs (RFC 4314) over the mailbox_acl table, mirroring the bbolt store's
// global "acl" bucket keyed by (owner, mailbox, grantee).

// AuthenticateUser is not served by the storage layer — like the bbolt store it
// defers to the injected auth function (the account credentials live in the
// account store / LDAP, wired via SetAuthFunc).
func (d *DB) AuthenticateUser(username, password string) (bool, error) {
	return false, fmt.Errorf("AuthenticateUser: not implemented, use SetAuthFunc")
}

// GetACL returns the grantee's rights on the mailbox (0 when none).
func (d *DB) GetACL(owner, mailbox, grantee string) (storage.ACLRights, error) {
	ctx := context.Background()
	var rights int16
	err := d.pool.QueryRow(ctx,
		`SELECT rights FROM mailbox_acl WHERE owner_email=$1 AND mailbox=$2 AND grantee=$3`,
		owner, mailbox, grantee,
	).Scan(&rights)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("postgres: get acl %s/%s/%s: %w", owner, mailbox, grantee, err)
	}
	return storage.ACLRights(rights), nil
}

// SetACL upserts a grant, or removes it when rights == 0 (bbolt parity).
func (d *DB) SetACL(owner, mailbox, grantee string, rights storage.ACLRights, grantingUser string) error {
	ctx := context.Background()
	if rights == 0 {
		if _, err := d.pool.Exec(ctx,
			`DELETE FROM mailbox_acl WHERE owner_email=$1 AND mailbox=$2 AND grantee=$3`,
			owner, mailbox, grantee,
		); err != nil {
			return fmt.Errorf("postgres: clear acl %s/%s/%s: %w", owner, mailbox, grantee, err)
		}
		return nil
	}
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO mailbox_acl (owner_email, mailbox, grantee, rights, granted_by)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (owner_email, mailbox, grantee)
		DO UPDATE SET rights=EXCLUDED.rights, granted_by=EXCLUDED.granted_by, granted_at=now()`,
		owner, mailbox, grantee, int16(rights), grantingUser,
	); err != nil {
		return fmt.Errorf("postgres: set acl %s/%s/%s: %w", owner, mailbox, grantee, err)
	}
	return nil
}

// DeleteACL removes one grant (grantee set) or all grants for the mailbox
// (grantee empty), matching the bbolt store.
func (d *DB) DeleteACL(owner, mailbox, grantee string) error {
	ctx := context.Background()
	var err error
	if grantee != "" {
		_, err = d.pool.Exec(ctx,
			`DELETE FROM mailbox_acl WHERE owner_email=$1 AND mailbox=$2 AND grantee=$3`,
			owner, mailbox, grantee)
	} else {
		_, err = d.pool.Exec(ctx,
			`DELETE FROM mailbox_acl WHERE owner_email=$1 AND mailbox=$2`, owner, mailbox)
	}
	if err != nil {
		return fmt.Errorf("postgres: delete acl %s/%s: %w", owner, mailbox, err)
	}
	return nil
}

// ListACL returns the mailbox's ACL entries.
func (d *DB) ListACL(owner, mailbox string) ([]storage.ACLEntry, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT grantee, rights, granted_at, granted_by FROM mailbox_acl
		 WHERE owner_email=$1 AND mailbox=$2 ORDER BY grantee`, owner, mailbox)
	if err != nil {
		return nil, fmt.Errorf("postgres: list acl %s/%s: %w", owner, mailbox, err)
	}
	defer rows.Close()
	var entries []storage.ACLEntry
	for rows.Next() {
		var e storage.ACLEntry
		var rights int16
		if err := rows.Scan(&e.Grantee, &rights, &e.GrantedAt, &e.GrantedBy); err != nil {
			return nil, fmt.Errorf("postgres: scan acl: %w", err)
		}
		e.Rights = storage.ACLRights(rights)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list acl %s/%s: %w", owner, mailbox, err)
	}
	return entries, nil
}

// ListMailboxesSharedWith returns "owner:mailbox" for every mailbox shared with
// the given grantee.
func (d *DB) ListMailboxesSharedWith(user string) ([]string, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT owner_email, mailbox FROM mailbox_acl WHERE grantee=$1 ORDER BY owner_email, mailbox`, user)
	if err != nil {
		return nil, fmt.Errorf("postgres: list shared-with %q: %w", user, err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var owner, mailbox string
		if err := rows.Scan(&owner, &mailbox); err != nil {
			return nil, fmt.Errorf("postgres: scan shared-with: %w", err)
		}
		result = append(result, owner+":"+mailbox)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list shared-with %q: %w", user, err)
	}
	return result, nil
}

// ListGranteesMailboxes returns the distinct mailbox names the owner has shared.
func (d *DB) ListGranteesMailboxes(owner string) ([]string, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT DISTINCT mailbox FROM mailbox_acl WHERE owner_email=$1 ORDER BY mailbox`, owner)
	if err != nil {
		return nil, fmt.Errorf("postgres: list shared mailboxes %q: %w", owner, err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var mailbox string
		if err := rows.Scan(&mailbox); err != nil {
			return nil, fmt.Errorf("postgres: scan shared mailbox: %w", err)
		}
		result = append(result, mailbox)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list shared mailboxes %q: %w", owner, err)
	}
	return result, nil
}
