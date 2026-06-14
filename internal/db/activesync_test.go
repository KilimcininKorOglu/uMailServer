package db

import (
	"errors"
	"testing"
)

// TestEASDeviceStore covers the EAS device-partnership store contract the
// ActiveSync Provision path relies on: an absent partnership is reported as
// ErrNotFound (so the handler can tell "never provisioned" from a real error),
// PutEASDevice upserts (re-provisioning rotates the policy key in place),
// listing is scoped to one owner, and delete removes exactly one partnership.
func TestEASDeviceStore(t *testing.T) {
	database, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	}()

	// Absent partnership must surface as ErrNotFound, not a generic error: the
	// Provision handler distinguishes "no partnership yet" from a storage fault.
	if _, err := database.GetEASDevice("bob@x.test", "DEV1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetEASDevice(absent) error = %v, want ErrNotFound", err)
	}

	dev := &EASDevice{Email: "bob@x.test", DeviceID: "DEV1", DeviceType: "iPhone", PolicyKey: "12345", ProtocolVersion: "16.1"}
	if err := database.PutEASDevice(dev); err != nil {
		t.Fatalf("PutEASDevice: %v", err)
	}
	got, err := database.GetEASDevice("bob@x.test", "DEV1")
	if err != nil {
		t.Fatalf("GetEASDevice: %v", err)
	}
	if got.PolicyKey != "12345" || got.ProtocolVersion != "16.1" || got.DeviceType != "iPhone" {
		t.Fatalf("GetEASDevice = %+v, want PolicyKey=12345 ProtocolVersion=16.1 DeviceType=iPhone", got)
	}

	// Re-provisioning rotates the policy key on the same (email, device) row.
	dev.PolicyKey = "67890"
	if err := database.PutEASDevice(dev); err != nil {
		t.Fatalf("PutEASDevice (update): %v", err)
	}
	got, err = database.GetEASDevice("bob@x.test", "DEV1")
	if err != nil {
		t.Fatalf("GetEASDevice (after re-provision): %v", err)
	}
	if got.PolicyKey != "67890" {
		t.Fatalf("PolicyKey after re-provision = %q, want 67890", got.PolicyKey)
	}

	// A second device for bob, and one for another user, must not bleed across.
	mustPut(t, database, &EASDevice{Email: "bob@x.test", DeviceID: "DEV2", PolicyKey: "p2"})
	mustPut(t, database, &EASDevice{Email: "alice@x.test", DeviceID: "DEV1", PolicyKey: "p3"})
	if list, err := database.ListEASDevicesByEmail("bob@x.test"); err != nil || len(list) != 2 {
		t.Fatalf("ListEASDevicesByEmail(bob) = %d devices (err=%v), want 2", len(list), err)
	}

	// Delete removes exactly the one partnership; the sibling survives.
	if err := database.DeleteEASDevice("bob@x.test", "DEV1"); err != nil {
		t.Fatalf("DeleteEASDevice: %v", err)
	}
	if _, err := database.GetEASDevice("bob@x.test", "DEV1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetEASDevice after delete error = %v, want ErrNotFound", err)
	}
	remaining, err := database.ListEASDevicesByEmail("bob@x.test")
	if err != nil {
		t.Fatalf("ListEASDevicesByEmail after delete: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("after delete, bob has %d devices, want 1", len(remaining))
	}
}

func mustPut(t *testing.T, d *DB, dev *EASDevice) {
	t.Helper()
	if err := d.PutEASDevice(dev); err != nil {
		t.Fatalf("PutEASDevice(%s/%s): %v", dev.Email, dev.DeviceID, err)
	}
}
