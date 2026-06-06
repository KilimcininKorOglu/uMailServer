package db

import (
	"github.com/umailserver/umailserver/internal/vacation"
)

// Typed accessors for the data the bbolt store kept as opaque JSON under the
// preferences and vacation buckets. They wrap the existing generic Get/Put so
// the on-disk encoding is byte-identical to what the handlers wrote directly —
// the relational backend (internal/db/postgres) offers the same method set
// backed by typed tables, so callers depend on the methods, not the engine.

// signatureValue mirrors the webmail handler's signature struct so the stored
// JSON ({"signature":"..."}) is unchanged.
type signatureValue struct {
	Signature string `json:"signature"`
}

// categoriesValue mirrors the webmail handler's categories struct.
type categoriesValue struct {
	Categories []Category `json:"categories"`
}

const (
	signatureKeySuffix  = ":signature"
	categoriesKeySuffix = ":categories"
	// userConfigKeyPrefix matches the EWS handler's bbolt key prefix so existing
	// UserConfiguration entries round-trip.
	userConfigKeyPrefix = "ewsuserconfig:"
)

// GetUIPrefs returns the user's webmail toggle map, empty when none are set
// (matching the handler's treat-error-as-empty behavior).
func (d *DB) GetUIPrefs(user string) (map[string]bool, error) {
	prefs := map[string]bool{}
	if err := d.Get(BucketPreferences, user, &prefs); err != nil {
		return map[string]bool{}, nil
	}
	return prefs, nil
}

// PutUIPrefs stores the user's toggle map.
func (d *DB) PutUIPrefs(user string, prefs map[string]bool) error {
	return d.Put(BucketPreferences, user, prefs)
}

// GetSignature returns the user's signature, or "" when unset.
func (d *DB) GetSignature(user string) (string, error) {
	var v signatureValue
	if err := d.Get(BucketPreferences, user+signatureKeySuffix, &v); err != nil {
		return "", nil
	}
	return v.Signature, nil
}

// PutSignature stores the user's signature.
func (d *DB) PutSignature(user, signature string) error {
	return d.Put(BucketPreferences, user+signatureKeySuffix, signatureValue{Signature: signature})
}

// GetCategories returns the user's categories, empty when unset.
func (d *DB) GetCategories(user string) ([]Category, error) {
	var v categoriesValue
	if err := d.Get(BucketPreferences, user+categoriesKeySuffix, &v); err != nil {
		return nil, nil
	}
	return v.Categories, nil
}

// PutCategories stores the user's categories.
func (d *DB) PutCategories(user string, categories []Category) error {
	return d.Put(BucketPreferences, user+categoriesKeySuffix, categoriesValue{Categories: categories})
}

// GetVacation returns the user's vacation config, erroring when none is stored
// so the caller can fall back to its default.
func (d *DB) GetVacation(user string) (*vacation.Config, error) {
	var c vacation.Config
	if err := d.Get(BucketVacation, user, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// PutVacation stores the user's vacation config.
func (d *DB) PutVacation(user string, c *vacation.Config) error {
	return d.Put(BucketVacation, user, c)
}

// DeleteVacation removes the user's vacation config.
func (d *DB) DeleteVacation(user string) error {
	return d.Delete(BucketVacation, user)
}

// GetUserConfig returns the Outlook EWS UserConfiguration at (owner, name),
// erroring when absent.
func (d *DB) GetUserConfig(owner, name string) (*UserConfigBlob, error) {
	var b UserConfigBlob
	if err := d.Get(BucketPreferences, userConfigKey(owner, name), &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// PutUserConfig stores the EWS UserConfiguration at (owner, name).
func (d *DB) PutUserConfig(owner, name string, b *UserConfigBlob) error {
	return d.Put(BucketPreferences, userConfigKey(owner, name), *b)
}

// DeleteUserConfig removes the EWS UserConfiguration at (owner, name).
func (d *DB) DeleteUserConfig(owner, name string) error {
	return d.Delete(BucketPreferences, userConfigKey(owner, name))
}

// userConfigKey composes the bbolt key for an EWS UserConfiguration, matching
// the layout the EWS handler used (prefix + owner + ":" + name).
func userConfigKey(owner, name string) string {
	return userConfigKeyPrefix + owner + ":" + name
}
