package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core policy store: inbox rules, out-of-office, resource booking
// policies, and room lists. Mirrors *semcore.BoltPolicyStore. Scalar fields are
// typed columns; a rule's variant condition/action lists are JSONB payloads.

// --- rules ---

// ListRules returns a mailbox's rules sorted by priority (ascending).
func (d *DB) ListRules(mailboxID semcore.MailboxId) ([]*semcore.Rule, error) {
	return d.queryRules(`SELECT id, mailbox_id, change_key, name, enabled, priority, match_all, conditions, actions, created, modified
		FROM semcore_rule WHERE mailbox_id=$1`, mailboxID.String())
}

// ListAllRules returns every rule across mailboxes sorted by priority.
func (d *DB) ListAllRules() ([]*semcore.Rule, error) {
	return d.queryRules(`SELECT id, mailbox_id, change_key, name, enabled, priority, match_all, conditions, actions, created, modified
		FROM semcore_rule`)
}

func (d *DB) queryRules(sql string, args ...any) ([]*semcore.Rule, error) {
	rows, err := d.pool.Query(context.Background(), sql, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list rules: %w", err)
	}
	defer rows.Close()
	var result []*semcore.Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list rules: %w", err)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Priority < result[j].Priority })
	return result, nil
}

// GetRule returns a rule by id, or an error when absent.
func (d *DB) GetRule(id semcore.RuleId) (*semcore.Rule, error) {
	rule, err := scanRule(d.pool.QueryRow(context.Background(),
		`SELECT id, mailbox_id, change_key, name, enabled, priority, match_all, conditions, actions, created, modified
		 FROM semcore_rule WHERE id=$1`, id.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("rule not found: %s", id.String())
		}
		return nil, fmt.Errorf("postgres: get rule %s: %w", id, err)
	}
	return rule, nil
}

// PutRule upserts a rule, assigning a ChangeKey when the caller left it zero.
func (d *DB) PutRule(rule *semcore.Rule) error {
	if rule.ChangeKey.IsZero() {
		ck, err := semcore.NewRuleChangeKey(newSemcoreID())
		if err != nil {
			return err
		}
		rule.ChangeKey = ck
	}
	conds, err := json.Marshal(rule.Conditions)
	if err != nil {
		return fmt.Errorf("postgres: marshal rule conditions: %w", err)
	}
	acts, err := json.Marshal(rule.Actions)
	if err != nil {
		return fmt.Errorf("postgres: marshal rule actions: %w", err)
	}
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_rule (id, mailbox_id, change_key, name, enabled, priority, match_all, conditions, actions, created, modified)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO UPDATE SET mailbox_id=EXCLUDED.mailbox_id, change_key=EXCLUDED.change_key,
			name=EXCLUDED.name, enabled=EXCLUDED.enabled, priority=EXCLUDED.priority,
			match_all=EXCLUDED.match_all, conditions=EXCLUDED.conditions, actions=EXCLUDED.actions,
			created=EXCLUDED.created, modified=EXCLUDED.modified`,
		rule.ID.String(), rule.MailboxID.String(), rule.ChangeKey.String(), rule.Name, rule.Enabled,
		rule.Priority, rule.MatchAll, conds, acts, rule.Created, rule.Modified,
	); err != nil {
		return fmt.Errorf("postgres: put rule %s: %w", rule.ID, err)
	}
	return nil
}

// DeleteRule removes a rule (no error when absent, matching the bbolt store).
func (d *DB) DeleteRule(id semcore.RuleId) error {
	if _, err := d.pool.Exec(context.Background(), `DELETE FROM semcore_rule WHERE id=$1`, id.String()); err != nil {
		return fmt.Errorf("postgres: delete rule %s: %w", id, err)
	}
	return nil
}

func scanRule(row rowScanner) (*semcore.Rule, error) {
	var id, mailboxID, changeKey, name string
	var enabled, matchAll bool
	var priority int
	var conds, acts []byte
	var created, modified time.Time
	if err := row.Scan(&id, &mailboxID, &changeKey, &name, &enabled, &priority, &matchAll, &conds, &acts, &created, &modified); err != nil {
		return nil, err
	}
	rid, err := semcore.NewRuleId(id)
	if err != nil {
		return nil, fmt.Errorf("invalid rule id %q: %w", id, err)
	}
	rule := &semcore.Rule{
		ID:        rid,
		MailboxID: parseMailboxID(mailboxID),
		Name:      name,
		Enabled:   enabled,
		Priority:  priority,
		MatchAll:  matchAll,
		Created:   created,
		Modified:  modified,
	}
	if changeKey != "" {
		if ck, err := semcore.NewRuleChangeKey(changeKey); err == nil {
			rule.ChangeKey = ck
		}
	}
	if len(conds) > 0 {
		if err := json.Unmarshal(conds, &rule.Conditions); err != nil {
			return nil, fmt.Errorf("unmarshal rule conditions: %w", err)
		}
	}
	if len(acts) > 0 {
		if err := json.Unmarshal(acts, &rule.Actions); err != nil {
			return nil, fmt.Errorf("unmarshal rule actions: %w", err)
		}
	}
	return rule, nil
}

// --- out-of-office ---

// GetOOF returns the OOF policy by id (OOFId == MailboxId), or an error.
func (d *DB) GetOOF(id semcore.OOFId) (*semcore.OOFPolicy, error) {
	var mailboxID, changeKey, state, timezone, subject, textBody, htmlBody, internalReply, externalReply string
	var enabled, ignoreLists, ignoreBulk, ignoreAutoReplies bool
	var replyStyle, audience int16
	var excludeAddresses []string
	var sendInterval int64
	var startTime, endTime *time.Time
	err := d.pool.QueryRow(context.Background(), `
		SELECT mailbox_id, change_key, enabled, state, start_time, end_time, timezone, subject,
			text_body, html_body, reply_style, internal_reply, external_reply, audience,
			exclude_addresses, ignore_lists, ignore_bulk, ignore_auto_replies, send_interval_seconds
		FROM semcore_oof WHERE id=$1`, id.String(),
	).Scan(&mailboxID, &changeKey, &enabled, &state, &startTime, &endTime, &timezone, &subject,
		&textBody, &htmlBody, &replyStyle, &internalReply, &externalReply, &audience,
		&excludeAddresses, &ignoreLists, &ignoreBulk, &ignoreAutoReplies, &sendInterval)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("OOF policy not found: %s", id.String())
		}
		return nil, fmt.Errorf("postgres: get oof %s: %w", id, err)
	}
	p := &semcore.OOFPolicy{
		ID:                  id,
		MailboxID:           parseMailboxID(mailboxID),
		Enabled:             enabled,
		State:               state,
		Timezone:            timezone,
		Subject:             subject,
		TextBody:            textBody,
		HTMLBody:            htmlBody,
		ReplyStyle:          semcore.OOFAutoReplyStyle(uint8(replyStyle)),
		InternalReply:       internalReply,
		ExternalReply:       externalReply,
		Audience:            semcore.OOFAudience(uint8(audience)),
		ExcludeAddresses:    excludeAddresses,
		IgnoreLists:         ignoreLists,
		IgnoreBulk:          ignoreBulk,
		IgnoreAutoReplies:   ignoreAutoReplies,
		SendIntervalSeconds: sendInterval,
	}
	if changeKey != "" {
		if ck, err := semcore.NewOOFChangeKey(changeKey); err == nil {
			p.ChangeKey = ck
		}
	}
	if startTime != nil {
		p.StartTime = *startTime
	}
	if endTime != nil {
		p.EndTime = *endTime
	}
	return p, nil
}

// PutOOF upserts an OOF policy, assigning a ChangeKey when left zero.
func (d *DB) PutOOF(p *semcore.OOFPolicy) error {
	if p.ChangeKey.IsZero() {
		ck, err := semcore.NewOOFChangeKey(newSemcoreID())
		if err != nil {
			return err
		}
		p.ChangeKey = ck
	}
	excludes := p.ExcludeAddresses
	if excludes == nil {
		excludes = []string{}
	}
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_oof (id, mailbox_id, change_key, enabled, state, start_time, end_time, timezone,
			subject, text_body, html_body, reply_style, internal_reply, external_reply, audience,
			exclude_addresses, ignore_lists, ignore_bulk, ignore_auto_replies, send_interval_seconds)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		ON CONFLICT (id) DO UPDATE SET mailbox_id=EXCLUDED.mailbox_id, change_key=EXCLUDED.change_key,
			enabled=EXCLUDED.enabled, state=EXCLUDED.state, start_time=EXCLUDED.start_time,
			end_time=EXCLUDED.end_time, timezone=EXCLUDED.timezone, subject=EXCLUDED.subject,
			text_body=EXCLUDED.text_body, html_body=EXCLUDED.html_body, reply_style=EXCLUDED.reply_style,
			internal_reply=EXCLUDED.internal_reply, external_reply=EXCLUDED.external_reply,
			audience=EXCLUDED.audience, exclude_addresses=EXCLUDED.exclude_addresses,
			ignore_lists=EXCLUDED.ignore_lists, ignore_bulk=EXCLUDED.ignore_bulk,
			ignore_auto_replies=EXCLUDED.ignore_auto_replies, send_interval_seconds=EXCLUDED.send_interval_seconds`,
		p.ID.String(), p.MailboxID.String(), p.ChangeKey.String(), p.Enabled, p.State,
		nullTime(p.StartTime), nullTime(p.EndTime), p.Timezone, p.Subject, p.TextBody, p.HTMLBody,
		int16(p.ReplyStyle), p.InternalReply, p.ExternalReply, int16(p.Audience), excludes,
		p.IgnoreLists, p.IgnoreBulk, p.IgnoreAutoReplies, p.SendIntervalSeconds,
	); err != nil {
		return fmt.Errorf("postgres: put oof %s: %w", p.ID, err)
	}
	return nil
}

// --- resources ---

// ListResources returns all resource policies.
func (d *DB) ListResources() ([]*semcore.ResourcePolicy, error) {
	rows, err := d.pool.Query(context.Background(), `SELECT `+resourceCols+` FROM semcore_resource ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list resources: %w", err)
	}
	defer rows.Close()
	var result []*semcore.ResourcePolicy
	for rows.Next() {
		p, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list resources: %w", err)
	}
	return result, nil
}

// GetResource returns a resource policy by id, or an error.
func (d *DB) GetResource(id semcore.ResourceId) (*semcore.ResourcePolicy, error) {
	p, err := scanResource(d.pool.QueryRow(context.Background(),
		`SELECT `+resourceCols+` FROM semcore_resource WHERE id=$1`, id.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("resource policy not found: %s", id.String())
		}
		return nil, fmt.Errorf("postgres: get resource %s: %w", id, err)
	}
	return p, nil
}

// PutResource upserts a resource policy, assigning a ChangeKey when left zero.
func (d *DB) PutResource(p *semcore.ResourcePolicy) error {
	if p.ChangeKey.IsZero() {
		ck, err := semcore.NewResourceChangeKey(newSemcoreID())
		if err != nil {
			return err
		}
		p.ChangeKey = ck
	}
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_resource (id, mailbox_id, change_key, name, kind, email, capacity, description,
			decision, delegate_email, allow_recurring, max_duration_minutes, min_notice_minutes,
			allow_conflicts, max_conflicts, hidden_from_gal, created, modified)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (id) DO UPDATE SET mailbox_id=EXCLUDED.mailbox_id, change_key=EXCLUDED.change_key,
			name=EXCLUDED.name, kind=EXCLUDED.kind, email=EXCLUDED.email, capacity=EXCLUDED.capacity,
			description=EXCLUDED.description, decision=EXCLUDED.decision, delegate_email=EXCLUDED.delegate_email,
			allow_recurring=EXCLUDED.allow_recurring, max_duration_minutes=EXCLUDED.max_duration_minutes,
			min_notice_minutes=EXCLUDED.min_notice_minutes, allow_conflicts=EXCLUDED.allow_conflicts,
			max_conflicts=EXCLUDED.max_conflicts, hidden_from_gal=EXCLUDED.hidden_from_gal,
			created=EXCLUDED.created, modified=EXCLUDED.modified`,
		p.ID.String(), p.MailboxID.String(), p.ChangeKey.String(), p.Name, int16(p.Kind), p.Email,
		p.Capacity, p.Description, int16(p.Decision), p.DelegateEmail, p.AllowRecurring,
		p.MaxDurationMinutes, p.MinNoticeMinutes, p.AllowConflicts, p.MaxConflicts, p.HiddenFromGAL,
		p.Created, p.Modified,
	); err != nil {
		return fmt.Errorf("postgres: put resource %s: %w", p.ID, err)
	}
	return nil
}

// DeleteResource removes a resource policy (no error when absent).
func (d *DB) DeleteResource(id semcore.ResourceId) error {
	if _, err := d.pool.Exec(context.Background(), `DELETE FROM semcore_resource WHERE id=$1`, id.String()); err != nil {
		return fmt.Errorf("postgres: delete resource %s: %w", id, err)
	}
	return nil
}

const resourceCols = `id, mailbox_id, change_key, name, kind, email, capacity, description,
	decision, delegate_email, allow_recurring, max_duration_minutes, min_notice_minutes,
	allow_conflicts, max_conflicts, hidden_from_gal, created, modified`

func scanResource(row rowScanner) (*semcore.ResourcePolicy, error) {
	var id, mailboxID, changeKey, name, email, description, delegateEmail string
	var kind, decision int16
	var capacity, maxDuration, minNotice, maxConflicts int
	var allowRecurring, allowConflicts, hiddenFromGAL bool
	var created, modified time.Time
	if err := row.Scan(&id, &mailboxID, &changeKey, &name, &kind, &email, &capacity, &description,
		&decision, &delegateEmail, &allowRecurring, &maxDuration, &minNotice,
		&allowConflicts, &maxConflicts, &hiddenFromGAL, &created, &modified); err != nil {
		return nil, err
	}
	rid, err := semcore.NewResourceId(id)
	if err != nil {
		return nil, fmt.Errorf("invalid resource id %q: %w", id, err)
	}
	p := &semcore.ResourcePolicy{
		ID:                 rid,
		MailboxID:          parseMailboxID(mailboxID),
		Name:               name,
		Kind:               semcore.ResourceKind(uint8(kind)),
		Email:              email,
		Capacity:           capacity,
		Description:        description,
		Decision:           semcore.BookingDecision(uint8(decision)),
		DelegateEmail:      delegateEmail,
		AllowRecurring:     allowRecurring,
		MaxDurationMinutes: maxDuration,
		MinNoticeMinutes:   minNotice,
		AllowConflicts:     allowConflicts,
		MaxConflicts:       maxConflicts,
		HiddenFromGAL:      hiddenFromGAL,
		Created:            created,
		Modified:           modified,
	}
	if changeKey != "" {
		if ck, err := semcore.NewResourceChangeKey(changeKey); err == nil {
			p.ChangeKey = ck
		}
	}
	return p, nil
}

// --- room lists ---

// ListRoomLists returns all room lists.
func (d *DB) ListRoomLists() ([]*semcore.RoomList, error) {
	rows, err := d.pool.Query(context.Background(),
		`SELECT id, name, rooms, created, modified FROM semcore_room_list ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list room lists: %w", err)
	}
	defer rows.Close()
	var result []*semcore.RoomList
	for rows.Next() {
		rl, err := scanRoomList(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, rl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list room lists: %w", err)
	}
	return result, nil
}

// GetRoomList returns a room list by id, or an error.
func (d *DB) GetRoomList(id string) (*semcore.RoomList, error) {
	rl, err := scanRoomList(d.pool.QueryRow(context.Background(),
		`SELECT id, name, rooms, created, modified FROM semcore_room_list WHERE id=$1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("room list not found: %s", id)
		}
		return nil, fmt.Errorf("postgres: get room list %s: %w", id, err)
	}
	return rl, nil
}

// PutRoomList upserts a room list.
func (d *DB) PutRoomList(rl *semcore.RoomList) error {
	rooms := rl.Rooms
	if rooms == nil {
		rooms = []string{}
	}
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_room_list (id, name, rooms, created, modified)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, rooms=EXCLUDED.rooms,
			created=EXCLUDED.created, modified=EXCLUDED.modified`,
		rl.ID, rl.Name, rooms, rl.Created, rl.Modified,
	); err != nil {
		return fmt.Errorf("postgres: put room list %s: %w", rl.ID, err)
	}
	return nil
}

// DeleteRoomList removes a room list (no error when absent).
func (d *DB) DeleteRoomList(id string) error {
	if _, err := d.pool.Exec(context.Background(), `DELETE FROM semcore_room_list WHERE id=$1`, id); err != nil {
		return fmt.Errorf("postgres: delete room list %s: %w", id, err)
	}
	return nil
}

func scanRoomList(row rowScanner) (*semcore.RoomList, error) {
	var id, name string
	var rooms []string
	var created, modified time.Time
	if err := row.Scan(&id, &name, &rooms, &created, &modified); err != nil {
		return nil, err
	}
	return &semcore.RoomList{ID: id, Name: name, Rooms: rooms, Created: created, Modified: modified}, nil
}
