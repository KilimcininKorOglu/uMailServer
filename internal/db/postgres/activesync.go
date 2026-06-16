package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/umailserver/umailserver/internal/db"
)

// This file implements the db.Store Exchange ActiveSync device-partnership
// methods on the PostgreSQL backend, keyed by (email, device_id) — the
// counterpart of the bbolt implementation in internal/db/db.go.

const easDeviceSelect = `
	SELECT email, device_id, device_type, user_agent, policy_key, protocol_version,
		wipe_requested, model, imei, friendly_name, os, os_language, phone_number,
		mobile_operator, first_sync, last_sync
	FROM activesync_devices`

func scanEASDevice(row rowScanner) (*db.EASDevice, error) {
	var e db.EASDevice
	if err := row.Scan(&e.Email, &e.DeviceID, &e.DeviceType, &e.UserAgent, &e.PolicyKey,
		&e.ProtocolVersion, &e.WipeRequested, &e.Model, &e.IMEI, &e.FriendlyName, &e.OS,
		&e.OSLanguage, &e.PhoneNumber, &e.MobileOperator, &e.FirstSync, &e.LastSync); err != nil {
		return nil, err
	}
	return &e, nil
}

// PutEASDevice upserts an EAS device partnership keyed by (email, device_id).
func (d *DB) PutEASDevice(dev *db.EASDevice) error {
	ctx := context.Background()
	if dev.FirstSync.IsZero() {
		dev.FirstSync = time.Now()
	}
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO activesync_devices (email, device_id, device_type, user_agent,
			policy_key, protocol_version, wipe_requested, model, imei, friendly_name,
			os, os_language, phone_number, mobile_operator, first_sync, last_sync)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (email, device_id) DO UPDATE SET device_type=EXCLUDED.device_type,
			user_agent=EXCLUDED.user_agent, policy_key=EXCLUDED.policy_key,
			protocol_version=EXCLUDED.protocol_version,
			wipe_requested=EXCLUDED.wipe_requested, model=EXCLUDED.model,
			imei=EXCLUDED.imei, friendly_name=EXCLUDED.friendly_name, os=EXCLUDED.os,
			os_language=EXCLUDED.os_language, phone_number=EXCLUDED.phone_number,
			mobile_operator=EXCLUDED.mobile_operator, last_sync=EXCLUDED.last_sync`,
		dev.Email, dev.DeviceID, dev.DeviceType, dev.UserAgent, dev.PolicyKey,
		dev.ProtocolVersion, dev.WipeRequested, dev.Model, dev.IMEI, dev.FriendlyName,
		dev.OS, dev.OSLanguage, dev.PhoneNumber, dev.MobileOperator, dev.FirstSync, dev.LastSync,
	); err != nil {
		return fmt.Errorf("postgres: put EAS device %q/%q: %w", dev.Email, dev.DeviceID, err)
	}
	return nil
}

// GetEASDevice returns the partnership for (email, deviceID), or a wrapped
// ErrNotFound when none exists.
func (d *DB) GetEASDevice(email, deviceID string) (*db.EASDevice, error) {
	ctx := context.Background()
	e, err := scanEASDevice(d.pool.QueryRow(ctx, easDeviceSelect+` WHERE email=$1 AND device_id=$2`, email, deviceID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: EAS device %q/%q not found: %w", email, deviceID, db.ErrNotFound)
		}
		return nil, fmt.Errorf("postgres: get EAS device %q/%q: %w", email, deviceID, err)
	}
	return e, nil
}

// ListEASDevicesByEmail returns every partnership owned by email.
func (d *DB) ListEASDevicesByEmail(email string) ([]*db.EASDevice, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, easDeviceSelect+` WHERE email=$1 ORDER BY first_sync`, email)
	if err != nil {
		return nil, fmt.Errorf("postgres: list EAS devices for %q: %w", email, err)
	}
	defer rows.Close()
	var out []*db.EASDevice
	for rows.Next() {
		e, err := scanEASDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan EAS device: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list EAS devices for %q: %w", email, err)
	}
	return out, nil
}

// ListAllEASDevices returns every device partnership across all accounts.
// It is the unfiltered counterpart of ListEASDevicesByEmail and is used by
// admin views that aggregate last-sync activity across the deployment.
func (d *DB) ListAllEASDevices() ([]*db.EASDevice, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, easDeviceSelect+` ORDER BY last_sync DESC NULLS LAST`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list all EAS devices: %w", err)
	}
	defer rows.Close()
	var out []*db.EASDevice
	for rows.Next() {
		e, err := scanEASDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan EAS device: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list all EAS devices: %w", err)
	}
	return out, nil
}

// DeleteEASDevice removes an EAS device partnership.
func (d *DB) DeleteEASDevice(email, deviceID string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM activesync_devices WHERE email=$1 AND device_id=$2`, email, deviceID,
	); err != nil {
		return fmt.Errorf("postgres: delete EAS device %q/%q: %w", email, deviceID, err)
	}
	return nil
}
