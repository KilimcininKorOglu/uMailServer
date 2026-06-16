package postgres

import (
	"context"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// SetSMIMEKeys stores or overwrites a user's S/MIME certificate and private key.
func (d *DB) SetSMIMEKeys(userID, certPEM, keyPEM string) error {
	_, err := d.pool.Exec(context.Background(), `
		INSERT INTO user_smime_keys (user_id, cert_pem, key_pem, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET
			cert_pem = EXCLUDED.cert_pem,
			key_pem  = EXCLUDED.key_pem,
			updated_at = EXCLUDED.updated_at`,
		userID, certPEM, keyPEM, time.Now())
	return err
}

// GetSMIMEKeys returns the stored certificate and key for a user.
// Returns a wrapped db.ErrNotFound when the user has no keys.
func (d *DB) GetSMIMEKeys(userID string) (certPEM, keyPEM string, err error) {
	err = d.pool.QueryRow(context.Background(),
		"SELECT cert_pem, key_pem FROM user_smime_keys WHERE user_id = $1", userID,
	).Scan(&certPEM, &keyPEM)
	if err != nil {
		return "", "", db.ErrNotFound
	}
	return certPEM, keyPEM, nil
}

// DeleteSMIMEKeys removes the stored keys for a user.
func (d *DB) DeleteSMIMEKeys(userID string) error {
	_, err := d.pool.Exec(context.Background(),
		"DELETE FROM user_smime_keys WHERE user_id = $1", userID)
	return err
}

// HasSMIMEKeys reports whether a user has S/MIME keys stored.
func (d *DB) HasSMIMEKeys(userID string) (bool, error) {
	var exists bool
	err := d.pool.QueryRow(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM user_smime_keys WHERE user_id = $1)", userID,
	).Scan(&exists)
	return exists, err
}
