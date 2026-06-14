package api

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/audit"
	"github.com/umailserver/umailserver/internal/db"
)

// handleAccountDevices serves the Exchange ActiveSync device-partnership admin
// surface for one account, reached as a sub-path of the account detail route:
//
//	GET    /api/v1/accounts/{email}/devices                 list partnerships
//	DELETE /api/v1/accounts/{email}/devices/{deviceID}      remove a partnership
//	POST   /api/v1/accounts/{email}/devices/{deviceID}/wipe flag a remote wipe
//
// rest is the path after ".../devices" ("", "/{deviceID}", or
// "/{deviceID}/wipe"). Access is gated by the same per-account tenant scope as
// the rest of the account surface.
func (s *Server) handleAccountDevices(w http.ResponseWriter, r *http.Request, email, rest string) {
	if !s.mayAccessAccount(r, email) {
		s.sendError(w, http.StatusForbidden, "access denied")
		return
	}

	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		if r.Method != http.MethodGet {
			s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.listEASDevices(w, email)
		return
	}

	deviceID, action, _ := strings.Cut(rest, "/")
	switch action {
	case "":
		if r.Method != http.MethodDelete {
			s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.deleteEASDevice(w, email, deviceID)
	case "wipe":
		if r.Method != http.MethodPost {
			s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.wipeEASDevice(w, r, email, deviceID)
	default:
		s.sendError(w, http.StatusNotFound, "not found")
	}
}

// listEASDevices returns the account's EAS device partnerships. The PolicyKey is
// a per-device secret and is deliberately omitted from the projection.
func (s *Server) listEASDevices(w http.ResponseWriter, email string) {
	devices, err := s.db.ListEASDevicesByEmail(email)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list devices")
		return
	}
	out := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		out = append(out, easDeviceToJSON(d))
	}
	s.sendJSON(w, http.StatusOK, out)
}

// wipeEASDevice flags a device for remote wipe. The wipe is delivered on the
// device's next contact: the provisioning gate forces it to Provision, which
// returns the RemoteWipe directive (internal/activesync). The flag is durable
// so a device that is offline now is wiped whenever it returns.
func (s *Server) wipeEASDevice(w http.ResponseWriter, r *http.Request, email, deviceID string) {
	dev, err := s.db.GetEASDevice(email, deviceID)
	if err != nil || dev == nil {
		s.sendError(w, http.StatusNotFound, "device not found")
		return
	}
	dev.WipeRequested = true
	if err := s.db.PutEASDevice(dev); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to flag remote wipe")
		return
	}

	actor, ok := r.Context().Value("user").(string)
	if !ok || actor == "" {
		actor = "system"
	}
	if err := s.auditLogger.Log(audit.Event{
		Type:    audit.EASRemoteWipe,
		User:    actor,
		IP:      audit.ExtractIP(r),
		Success: true,
		Details: map[string]string{"target": email, "device_id": deviceID},
		Service: "api",
	}); err != nil {
		s.logger.Warn("audit log failed", "event", "eas_remote_wipe", "error", err)
	}

	s.sendJSON(w, http.StatusOK, easDeviceToJSON(dev))
}

// deleteEASDevice removes a device partnership outright (without a wipe). A
// device that returns afterward must provision from scratch.
func (s *Server) deleteEASDevice(w http.ResponseWriter, email, deviceID string) {
	if err := s.db.DeleteEASDevice(email, deviceID); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to delete device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// easDeviceToJSON projects a device partnership for the admin API, omitting the
// PolicyKey (a per-device secret).
func easDeviceToJSON(d *db.EASDevice) map[string]any {
	return map[string]any{
		"device_id":        d.DeviceID,
		"device_type":      d.DeviceType,
		"user_agent":       d.UserAgent,
		"protocol_version": d.ProtocolVersion,
		"wipe_requested":   d.WipeRequested,
		"first_sync":       d.FirstSync,
		"last_sync":        d.LastSync,
	}
}
