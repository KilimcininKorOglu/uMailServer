package postgres

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// CreateMailGroup inserts (or overwrites) a mail group and its static members,
// stamping timestamps like db.DB.CreateMailGroup.
func (d *DB) CreateMailGroup(group *db.MailGroup) error {
	now := time.Now()
	if group.CreatedAt.IsZero() {
		group.CreatedAt = now
	}
	group.UpdatedAt = now
	return d.upsertMailGroup(group)
}

// UpdateMailGroup re-stamps UpdatedAt and overwrites the group.
func (d *DB) UpdateMailGroup(group *db.MailGroup) error {
	group.UpdatedAt = time.Now()
	return d.upsertMailGroup(group)
}

func (d *DB) upsertMailGroup(g *db.MailGroup) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin upsert mail group: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(ctx, `
		INSERT INTO mail_groups (email, local_part, domain, description, is_active,
			dynamic, dynamic_domain, dynamic_admin_only, dynamic_local_pattern,
			sender_policy, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (email) DO UPDATE SET local_part=EXCLUDED.local_part,
			domain=EXCLUDED.domain, description=EXCLUDED.description,
			is_active=EXCLUDED.is_active, dynamic=EXCLUDED.dynamic,
			dynamic_domain=EXCLUDED.dynamic_domain,
			dynamic_admin_only=EXCLUDED.dynamic_admin_only,
			dynamic_local_pattern=EXCLUDED.dynamic_local_pattern,
			sender_policy=EXCLUDED.sender_policy, updated_at=EXCLUDED.updated_at`,
		g.Email, g.LocalPart, g.Domain, g.Description, g.IsActive,
		g.Dynamic, g.DynamicDomain, g.DynamicAdminOnly, g.DynamicLocalPattern,
		g.SenderPolicy, g.CreatedAt, g.UpdatedAt,
	); err != nil {
		return fmt.Errorf("postgres: upsert mail group %q: %w", g.Email, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mail_group_members WHERE group_email=$1`, g.Email); err != nil {
		return fmt.Errorf("postgres: clear mail group members %q: %w", g.Email, err)
	}
	for i, m := range g.Members {
		if _, err := tx.Exec(ctx,
			`INSERT INTO mail_group_members (group_email, ord, member) VALUES ($1,$2,$3)`,
			g.Email, i, m,
		); err != nil {
			return fmt.Errorf("postgres: insert mail group member %q[%d]: %w", g.Email, i, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit upsert mail group: %w", err)
	}
	return nil
}

// GetMailGroup returns the group at (domain, local part), case-insensitively
// (matching the bbolt lower-cased key), with its members. It errors when absent.
func (d *DB) GetMailGroup(domain, localPart string) (*db.MailGroup, error) {
	ctx := context.Background()
	g, err := scanMailGroup(d.pool.QueryRow(ctx,
		mailGroupSelect+` WHERE lower(domain)=lower($1) AND lower(local_part)=lower($2)`, domain, localPart))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: mail group %s/%s not found", domain, localPart)
		}
		return nil, fmt.Errorf("postgres: get mail group %s/%s: %w", domain, localPart, err)
	}
	if err := d.loadMembers(ctx, g); err != nil {
		return nil, err
	}
	return g, nil
}

// ListMailGroups returns every mail group with members, ordered by email.
func (d *DB) ListMailGroups() ([]*db.MailGroup, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, mailGroupSelect+` ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list mail groups: %w", err)
	}
	groups, err := collectMailGroups(rows)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if err := d.loadMembers(ctx, g); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

// DeleteMailGroup removes the group at (domain, local part); members cascade.
func (d *DB) DeleteMailGroup(domain, localPart string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM mail_groups WHERE lower(domain)=lower($1) AND lower(local_part)=lower($2)`,
		domain, localPart,
	); err != nil {
		return fmt.Errorf("postgres: delete mail group %s/%s: %w", domain, localPart, err)
	}
	return nil
}

// ExpandMailGroup resolves a group to its recipient addresses, identical to
// db.DB.ExpandMailGroup: static groups return their explicit members; dynamic
// groups scan active accounts and apply the admin-only and local-part criteria.
func (d *DB) ExpandMailGroup(group *db.MailGroup) ([]string, error) {
	if group == nil || !group.IsActive {
		return nil, nil
	}
	if !group.Dynamic {
		out := make([]string, 0, len(group.Members))
		for _, m := range group.Members {
			if m = strings.TrimSpace(m); m != "" {
				out = append(out, m)
			}
		}
		return out, nil
	}

	scanDomain := group.DynamicDomain
	if scanDomain == "" {
		scanDomain = group.Domain
	}
	accounts, err := d.ListAccountsByDomain(scanDomain)
	if err != nil {
		return nil, err
	}
	pattern := strings.ToLower(strings.TrimSpace(group.DynamicLocalPattern))
	out := make([]string, 0, len(accounts))
	for _, a := range accounts {
		if !a.IsActive {
			continue
		}
		if group.DynamicAdminOnly != nil && a.IsAdmin != *group.DynamicAdminOnly {
			continue
		}
		if pattern != "" {
			ok, mErr := path.Match(pattern, strings.ToLower(a.LocalPart))
			if mErr != nil || !ok {
				continue
			}
		}
		out = append(out, a.Email)
	}
	return out, nil
}

const mailGroupSelect = `
	SELECT email, local_part, domain, description, is_active, dynamic,
		dynamic_domain, dynamic_admin_only, dynamic_local_pattern, sender_policy,
		created_at, updated_at
	FROM mail_groups`

func scanMailGroup(row rowScanner) (*db.MailGroup, error) {
	var g db.MailGroup
	if err := row.Scan(&g.Email, &g.LocalPart, &g.Domain, &g.Description, &g.IsActive,
		&g.Dynamic, &g.DynamicDomain, &g.DynamicAdminOnly, &g.DynamicLocalPattern,
		&g.SenderPolicy, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	return &g, nil
}

func collectMailGroups(rows pgx.Rows) ([]*db.MailGroup, error) {
	defer rows.Close()
	var groups []*db.MailGroup
	for rows.Next() {
		g, err := scanMailGroup(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan mail group: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: read mail groups: %w", err)
	}
	return groups, nil
}

func (d *DB) loadMembers(ctx context.Context, g *db.MailGroup) error {
	rows, err := d.pool.Query(ctx,
		`SELECT member FROM mail_group_members WHERE group_email=$1 ORDER BY ord`, g.Email)
	if err != nil {
		return fmt.Errorf("postgres: load mail group members %q: %w", g.Email, err)
	}
	defer rows.Close()
	var members []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return fmt.Errorf("postgres: scan mail group member %q: %w", g.Email, err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: load mail group members %q: %w", g.Email, err)
	}
	g.Members = members
	return nil
}
