package api

import (
	"net/http"
	"sort"

	"github.com/umailserver/umailserver/internal/semcore"
)

// roomDTO is one bookable room offered to the calendar's room picker.
type roomDTO struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity,omitempty"`
}

// handleRooms lists the organization's bookable rooms for the calendar room
// picker. A room is offered when it is a room resource, not hidden from the
// GAL, and not configured to auto-decline every request.
// GET /api/v1/rooms
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := r.Context().Value("user").(string); !ok {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.semStore == nil {
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"rooms": []roomDTO{}})
		return
	}
	resources, err := s.semStore.Policy().ListResources()
	if err != nil {
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"rooms": []roomDTO{}})
		return
	}
	rooms := make([]roomDTO, 0, len(resources))
	for _, rp := range resources {
		if rp.Kind != semcore.ResourceKindRoom || rp.HiddenFromGAL {
			continue
		}
		if rp.Decision == semcore.BookingDecisionAutoDecline {
			continue
		}
		rooms = append(rooms, roomDTO{Email: rp.Email, Name: rp.Name, Capacity: rp.Capacity})
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Name < rooms[j].Name })
	s.sendJSON(w, http.StatusOK, map[string]interface{}{"rooms": rooms})
}

// roomLookup reports whether an address is a bookable room and whether it
// auto-accepts. It backs the calendar's resource auto-booking. ok is false for
// non-resource addresses and for rooms that auto-decline.
func (s *Server) roomLookup(email string) (autoAccept bool, ok bool) {
	if s.semStore == nil {
		return false, false
	}
	resID, err := semcore.NewResourceId(email)
	if err != nil {
		return false, false
	}
	rp, err := s.semStore.Policy().GetResource(resID)
	if err != nil || rp == nil {
		return false, false
	}
	if rp.Kind != semcore.ResourceKindRoom || rp.Decision == semcore.BookingDecisionAutoDecline {
		return false, false
	}
	return rp.Decision == semcore.BookingDecisionAutoAccept, true
}
