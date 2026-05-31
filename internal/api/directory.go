package api

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
)

// directoryObjectDTO mirrors the frontend DirectoryObject interface
// (web/admin/src/pages/Directory.tsx). Only room/equipment resources are
// represented; user accounts are managed on the Accounts page.
type directoryObjectDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Type       string `json:"type"` // "room" | "equipment"
	IsHidden   bool   `json:"isHidden"`
	IsBookable bool   `json:"isBookable"`
	Capacity   int    `json:"capacity,omitempty"`
}

// bookingPolicyDTO mirrors the frontend BookingPolicy interface. It is derived
// from the same ResourcePolicy that backs the directory object.
type bookingPolicyDTO struct {
	ID               string `json:"id"`
	ResourceName     string `json:"resourceName"`
	AutoAccept       bool   `json:"autoAccept"`
	AllowRecurring   bool   `json:"allowRecurring"`
	MaxDuration      int    `json:"maxDuration"`
	RequiresApproval bool   `json:"requiresApproval"`
	ApprovalDelegate string `json:"approvalDelegate"`
}

// roomListDTO mirrors the frontend RoomList interface.
type roomListDTO struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Rooms []string `json:"rooms"`
}

type directoryCreateRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Type     string `json:"type"` // "room" | "equipment"
	Capacity int    `json:"capacity"`
}

type directoryUpdateRequest struct {
	IsHidden         *bool   `json:"isHidden"`
	IsBookable       *bool   `json:"isBookable"`
	Capacity         *int    `json:"capacity"`
	AllowRecurring   *bool   `json:"allowRecurring"`
	MaxDuration      *int    `json:"maxDuration"`
	RequiresApproval *bool   `json:"requiresApproval"`
	ApprovalDelegate *string `json:"approvalDelegate"`
}

type roomListWriteRequest struct {
	Name  string   `json:"name"`
	Rooms []string `json:"rooms"`
}

// handleDirectory handles GET (list) and POST (create resource) on
// /api/v1/admin/directory.
func (s *Server) handleDirectory(w http.ResponseWriter, r *http.Request) {
	if s.semStore == nil {
		s.sendError(w, http.StatusServiceUnavailable, "directory store not available")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listDirectory(w, r)
	case http.MethodPost:
		s.createDirectoryResource(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleDirectoryDetail routes /api/v1/admin/directory/{...} to resource or
// room-list handlers. Room lists live under .../directory/roomlists[/{id}].
func (s *Server) handleDirectoryDetail(w http.ResponseWriter, r *http.Request) {
	if s.semStore == nil {
		s.sendError(w, http.StatusServiceUnavailable, "directory store not available")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/directory/")

	switch {
	case rest == "roomlists":
		s.handleRoomLists(w, r)
	case strings.HasPrefix(rest, "roomlists/"):
		s.handleRoomListDetail(w, r, strings.TrimPrefix(rest, "roomlists/"))
	default:
		s.handleResourceDetail(w, r, rest)
	}
}

// listDirectory returns all resources, their derived booking policies, and all
// room lists.
func (s *Server) listDirectory(w http.ResponseWriter, _ *http.Request) {
	resources, err := s.semStore.Policy().ListResources()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list resources")
		return
	}
	roomLists, err := s.semStore.Policy().ListRoomLists()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list room lists")
		return
	}

	objects := make([]directoryObjectDTO, 0, len(resources))
	policies := make([]bookingPolicyDTO, 0, len(resources))
	for _, rp := range resources {
		objects = append(objects, resourceToObjectDTO(rp))
		if rp.Kind == semcore.ResourceKindRoom {
			policies = append(policies, resourceToBookingDTO(rp))
		}
	}

	lists := make([]roomListDTO, 0, len(roomLists))
	for _, rl := range roomLists {
		lists = append(lists, roomListDTO{ID: rl.ID, Name: rl.Name, Rooms: rl.Rooms})
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"resources":        objects,
		"booking_policies": policies,
		"room_lists":       lists,
	})
}

// createDirectoryResource creates a room or equipment resource.
func (s *Server) createDirectoryResource(w http.ResponseWriter, r *http.Request) {
	var req directoryCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Name == "" || req.Email == "" {
		s.sendError(w, http.StatusBadRequest, "name and email are required")
		return
	}
	kind, ok := resourceKindFromString(req.Type)
	if !ok {
		s.sendError(w, http.StatusBadRequest, "type must be room or equipment")
		return
	}

	resID, err := semcore.NewResourceId(req.Email)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid resource email")
		return
	}
	mboxID, err := s.semStore.Identity().EnsureMailboxId(req.Email)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to resolve resource mailbox")
		return
	}

	rp := &semcore.ResourcePolicy{
		ID:        resID,
		MailboxID: mboxID,
		Name:      req.Name,
		Kind:      kind,
		Email:     req.Email,
		Capacity:  req.Capacity,
		Decision:  semcore.BookingDecisionAutoAccept, // bookable by default
	}
	if err := s.semStore.Policy().PutResource(rp); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to store resource")
		return
	}
	s.sendJSON(w, http.StatusCreated, resourceToObjectDTO(rp))
}

// handleResourceDetail handles PUT (update) and DELETE on a single resource.
func (s *Server) handleResourceDetail(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		s.sendError(w, http.StatusBadRequest, "resource id required")
		return
	}
	resID, err := semcore.NewResourceId(id)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid resource id")
		return
	}

	switch r.Method {
	case http.MethodPut:
		rp, err := s.semStore.Policy().GetResource(resID)
		if err != nil {
			s.sendError(w, http.StatusNotFound, "resource not found")
			return
		}
		var req directoryUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		applyResourceUpdate(rp, &req)
		if err := s.semStore.Policy().PutResource(rp); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to update resource")
			return
		}
		s.sendJSON(w, http.StatusOK, resourceToObjectDTO(rp))
	case http.MethodDelete:
		if err := s.semStore.Policy().DeleteResource(resID); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to delete resource")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleRoomLists handles GET (list) and POST (create) on
// /api/v1/admin/directory/roomlists.
func (s *Server) handleRoomLists(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		lists, err := s.semStore.Policy().ListRoomLists()
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to list room lists")
			return
		}
		out := make([]roomListDTO, 0, len(lists))
		for _, rl := range lists {
			out = append(out, roomListDTO{ID: rl.ID, Name: rl.Name, Rooms: rl.Rooms})
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"room_lists": out})
	case http.MethodPost:
		var req roomListWriteRequest
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			s.sendError(w, http.StatusBadRequest, "name is required")
			return
		}
		rl := &semcore.RoomList{Name: strings.TrimSpace(req.Name), Rooms: req.Rooms}
		if err := s.semStore.Policy().PutRoomList(rl); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to store room list")
			return
		}
		s.sendJSON(w, http.StatusCreated, roomListDTO{ID: rl.ID, Name: rl.Name, Rooms: rl.Rooms})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleRoomListDetail handles PUT (update) and DELETE on a single room list.
func (s *Server) handleRoomListDetail(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		s.sendError(w, http.StatusBadRequest, "room list id required")
		return
	}
	switch r.Method {
	case http.MethodPut:
		rl, err := s.semStore.Policy().GetRoomList(id)
		if err != nil {
			s.sendError(w, http.StatusNotFound, "room list not found")
			return
		}
		var req roomListWriteRequest
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(req.Name) != "" {
			rl.Name = strings.TrimSpace(req.Name)
		}
		if req.Rooms != nil {
			rl.Rooms = req.Rooms
		}
		if err := s.semStore.Policy().PutRoomList(rl); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to update room list")
			return
		}
		s.sendJSON(w, http.StatusOK, roomListDTO{ID: rl.ID, Name: rl.Name, Rooms: rl.Rooms})
	case http.MethodDelete:
		if err := s.semStore.Policy().DeleteRoomList(id); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to delete room list")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---------------------------------------------------------------------------
// Conversions between ResourcePolicy and the admin-UI DTOs
// ---------------------------------------------------------------------------

func resourceKindFromString(s string) (semcore.ResourceKind, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "room":
		return semcore.ResourceKindRoom, true
	case "equipment":
		return semcore.ResourceKindEquipment, true
	default:
		return 0, false
	}
}

// resourceToObjectDTO maps a ResourcePolicy to the directory object shape. A
// resource is "bookable" unless its booking decision auto-declines everything.
func resourceToObjectDTO(rp *semcore.ResourcePolicy) directoryObjectDTO {
	return directoryObjectDTO{
		ID:         rp.ID.String(),
		Name:       rp.Name,
		Email:      rp.Email,
		Type:       rp.Kind.String(),
		IsHidden:   rp.HiddenFromGAL,
		IsBookable: rp.Decision != semcore.BookingDecisionAutoDecline,
		Capacity:   rp.Capacity,
	}
}

func resourceToBookingDTO(rp *semcore.ResourcePolicy) bookingPolicyDTO {
	return bookingPolicyDTO{
		ID:               rp.ID.String(),
		ResourceName:     rp.Name,
		AutoAccept:       rp.Decision == semcore.BookingDecisionAutoAccept,
		AllowRecurring:   rp.AllowRecurring,
		MaxDuration:      rp.MaxDurationMinutes,
		RequiresApproval: rp.Decision == semcore.BookingDecisionDelegateReview,
		ApprovalDelegate: rp.DelegateEmail,
	}
}

// applyResourceUpdate mutates a ResourcePolicy from a partial update request.
// Booking decision is single-sourced: toggling bookable off auto-declines,
// requiresApproval routes to delegate review, otherwise auto-accept.
func applyResourceUpdate(rp *semcore.ResourcePolicy, req *directoryUpdateRequest) {
	if req.IsHidden != nil {
		rp.HiddenFromGAL = *req.IsHidden
	}
	if req.Capacity != nil {
		rp.Capacity = *req.Capacity
	}
	if req.AllowRecurring != nil {
		rp.AllowRecurring = *req.AllowRecurring
	}
	if req.MaxDuration != nil {
		rp.MaxDurationMinutes = *req.MaxDuration
	}
	if req.ApprovalDelegate != nil {
		rp.DelegateEmail = *req.ApprovalDelegate
	}
	// Decision is derived from bookable + requiresApproval toggles.
	switch {
	case req.IsBookable != nil && !*req.IsBookable:
		rp.Decision = semcore.BookingDecisionAutoDecline
	case req.RequiresApproval != nil && *req.RequiresApproval:
		rp.Decision = semcore.BookingDecisionDelegateReview
	case req.RequiresApproval != nil && !*req.RequiresApproval:
		rp.Decision = semcore.BookingDecisionAutoAccept
	case req.IsBookable != nil && *req.IsBookable && rp.Decision == semcore.BookingDecisionAutoDecline:
		rp.Decision = semcore.BookingDecisionAutoAccept
	}
}
