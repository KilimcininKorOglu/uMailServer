package api

import (
	"net/http"
	"sort"
	"time"
)

// syncActivityRowDTO is one device's last-sync snapshot, ordered by the
// handler from most-recent to oldest. The widget on the Diagnostics page
// renders a status badge off last_sync (fresh / recent / stale) and a
// tooltip showing the precise RFC3339 timestamp.
type syncActivityRowDTO struct {
	Email         string `json:"email"`
	DeviceID      string `json:"device_id"`
	DeviceType    string `json:"device_type"`
	FriendlyName  string `json:"friendly_name"`
	Protocol      string `json:"protocol"`
	LastSync      string `json:"last_sync"`        // RFC3339 or "" if never synced
	LastSyncUnix  int64  `json:"last_sync_unix"`   // 0 if LastSync is zero
	FreshnessDays int    `json:"freshness_days"`   // 0..N; large == stale
	Stale         bool   `json:"stale"`            // true if last_sync older than staleAfter
}

// syncActivitySummaryDTO is the top-level shape the widget consumes. It
// pairs the per-device list with deployment-level counts so the page can
// show "X devices, Y active in the last 24h" without a second round trip.
type syncActivitySummaryDTO struct {
	Total       int                   `json:"total"`
	Active1d    int                   `json:"active_1d"`
	Active7d    int                   `json:"active_7d"`
	Stale       int                   `json:"stale"`
	StaleAfter  string                `json:"stale_after"` // RFC3339 of the cutoff the widget calls stale
	Generated   string                `json:"generated"`   // RFC3339 of when the snapshot was taken
	Devices     []syncActivityRowDTO  `json:"devices"`
}

// syncStaleAfter is the cutoff beyond which a device is reported as "stale"
// in the admin widget. The choice mirrors the typical operator expectation
// that a healthy mobile client checks in at least once a day.
const syncStaleAfter = 7 * 24 * time.Hour

// handleAdminSyncActivity handles GET /api/v1/admin/sync/activity and
// returns a server-side snapshot of every EAS device's last-sync timestamp
// plus deployment-wide counts. The store is read-only; no writes happen.
func (s *Server) handleAdminSyncActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if s.db == nil {
		s.sendJSON(w, http.StatusOK, syncActivitySummaryDTO{
			Devices: []syncActivityRowDTO{},
		})
		return
	}

	devices, err := s.db.ListAllEASDevices()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "list eas devices: "+err.Error())
		return
	}

	now := time.Now()
	cutoff1d := now.Add(-24 * time.Hour)
	cutoff7d := now.Add(-7 * 24 * time.Hour)
	staleCutoff := now.Add(-syncStaleAfter)

	rows := make([]syncActivityRowDTO, 0, len(devices))
	var active1d, active7d, stale int
	for _, d := range devices {
		row := syncActivityRowDTO{
			Email:        d.Email,
			DeviceID:     d.DeviceID,
			DeviceType:   d.DeviceType,
			FriendlyName: d.FriendlyName,
			Protocol:     d.ProtocolVersion,
		}
		if !d.LastSync.IsZero() {
			row.LastSync = d.LastSync.UTC().Format(time.RFC3339)
			row.LastSyncUnix = d.LastSync.UTC().Unix()
			row.FreshnessDays = int(now.Sub(d.LastSync).Hours() / 24)
			if d.LastSync.After(cutoff1d) {
				active1d++
			}
			if d.LastSync.After(cutoff7d) {
				active7d++
			}
			if d.LastSync.Before(staleCutoff) {
				row.Stale = true
				stale++
			}
		} else {
			row.Stale = true
			stale++
		}
		rows = append(rows, row)
	}
	// Stable order: most recent first, then by email + device id.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].LastSyncUnix != rows[j].LastSyncUnix {
			return rows[i].LastSyncUnix > rows[j].LastSyncUnix
		}
		if rows[i].Email != rows[j].Email {
			return rows[i].Email < rows[j].Email
		}
		return rows[i].DeviceID < rows[j].DeviceID
	})

	s.sendJSON(w, http.StatusOK, syncActivitySummaryDTO{
		Total:      len(rows),
		Active1d:   active1d,
		Active7d:   active7d,
		Stale:      stale,
		StaleAfter: now.Add(-syncStaleAfter).UTC().Format(time.RFC3339),
		Generated:  now.UTC().Format(time.RFC3339),
		Devices:    rows,
	})
}
