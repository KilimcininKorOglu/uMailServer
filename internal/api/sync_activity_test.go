package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// stubEASStore is a minimal db.Store impl that returns the configured
// device list from ListAllEASDevices and ErrNotFound for everything else.
// It satisfies the interface only enough to exercise handleAdminSyncActivity
// in unit tests without spinning up a real bbolt or postgres backend.
type stubEASStore struct {
	db.Store
	devices []*db.EASDevice
}

func (s *stubEASStore) ListAllEASDevices() ([]*db.EASDevice, error) {
	return s.devices, nil
}

// TestHandleAdminSyncActivity_PostNotAllowed mirrors the other admin
// handlers' method gate.
func TestHandleAdminSyncActivity_PostNotAllowed(t *testing.T) {
	srv := &Server{db: &stubEASStore{}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/sync/activity", nil)
	w := httptest.NewRecorder()
	srv.handleAdminSyncActivity(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// TestHandleAdminSyncActivity_EmptyStore covers the no-devices-yet path.
func TestHandleAdminSyncActivity_EmptyStore(t *testing.T) {
	srv := &Server{db: &stubEASStore{devices: nil}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sync/activity", nil)
	w := httptest.NewRecorder()
	srv.handleAdminSyncActivity(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var body syncActivitySummaryDTO
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v; body=%s", err, w.Body.String())
	}
	if body.Total != 0 {
		t.Errorf("total = %d, want 0", body.Total)
	}
	if body.Devices == nil {
		t.Errorf("devices = nil, want []")
	}
	if len(body.Devices) != 0 {
		t.Errorf("len(devices) = %d, want 0", len(body.Devices))
	}
}

// TestHandleAdminSyncActivity_FreshnessBuckets feeds three devices at
// known ages (1h, 3d, 10d) and verifies the freshness counts and the
// per-row "stale" flag fall in the right buckets.
func TestHandleAdminSyncActivity_FreshnessBuckets(t *testing.T) {
	now := time.Now()
	srv := &Server{db: &stubEASStore{devices: []*db.EASDevice{
		{Email: "alice@x.test", DeviceID: "D-FRESH", DeviceType: "iPhone",
			FriendlyName: "Alice iPhone", ProtocolVersion: "16.1",
			LastSync: now.Add(-1 * time.Hour)},
		{Email: "bob@x.test", DeviceID: "D-3D", DeviceType: "Android",
			FriendlyName: "Bob Phone", ProtocolVersion: "16.1",
			LastSync: now.Add(-3 * 24 * time.Hour)},
		{Email: "carol@x.test", DeviceID: "D-STALE", DeviceType: "iPad",
			FriendlyName: "Carol iPad", ProtocolVersion: "14.1",
			LastSync: now.Add(-10 * 24 * time.Hour)},
	}}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sync/activity", nil)
	w := httptest.NewRecorder()
	srv.handleAdminSyncActivity(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	var body syncActivitySummaryDTO
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v; body=%s", err, w.Body.String())
	}
	if body.Total != 3 {
		t.Errorf("total = %d, want 3", body.Total)
	}
	if body.Active1d != 1 {
		t.Errorf("active_1d = %d, want 1", body.Active1d)
	}
	if body.Active7d != 2 {
		t.Errorf("active_7d = %d, want 2", body.Active7d)
	}
	if body.Stale != 1 {
		t.Errorf("stale = %d, want 1", body.Stale)
	}
	// Rows come back most-recent-first; assert the stale row sits at the
	// tail and carries the flag, and the fresh one leads.
	if len(body.Devices) != 3 {
		t.Fatalf("len(devices) = %d, want 3", len(body.Devices))
	}
	if body.Devices[0].LastSyncUnix < body.Devices[1].LastSyncUnix {
		t.Errorf("rows not sorted descending by last_sync_unix: %+v", body.Devices)
	}
	if !body.Devices[2].Stale {
		t.Errorf("oldest row should be stale, got %+v", body.Devices[2])
	}
	if body.Devices[2].DeviceID != "D-STALE" {
		t.Errorf("oldest row DeviceID = %q, want D-STALE", body.Devices[2].DeviceID)
	}
	if body.Devices[0].Stale {
		t.Errorf("newest row should not be stale, got %+v", body.Devices[0])
	}
}
