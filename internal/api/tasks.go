package api

import (
	"encoding/json"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/umailserver/umailserver/internal/caldav"
)

// TaskHandler bridges the webmail REST surface to VTODO items in the CalDAV
// store, stored in a dedicated "tasks" collection so they stay separate from
// calendar events. It mirrors CalendarHandler.
type TaskHandler struct {
	dataDir string
	// store is the canonical task store. When set (the semcore-backed
	// CollabTaskStore), webmail tasks live in the same collaboration "tasks"
	// folder EWS uses, so a task is identical across both surfaces. When nil it
	// falls back to the legacy filesystem store (tests / no semcore).
	store caldav.Store
}

// NewTaskHandler creates a task REST handler rooted at dataDir.
func NewTaskHandler(dataDir string) *TaskHandler {
	return &TaskHandler{dataDir: dataDir}
}

// SetStore wires the canonical task store (CollabTaskStore).
func (h *TaskHandler) SetStore(store caldav.Store) {
	h.store = store
}

// wireCollabTaskStore points the webmail task handler at the canonical
// collaboration task store so webmail tasks share one source of truth with EWS
// (both read/write the role-"tasks" folder). No-op until both are present.
func (s *Server) wireCollabTaskStore() {
	if s.semStore == nil || s.taskHandler == nil {
		return
	}
	s.taskHandler.SetStore(caldav.NewCollabTaskStore(s.semStore.Collaboration(), s.semStore.Identity()))
}

func (h *TaskHandler) getStorage() caldav.Store {
	if h.store != nil {
		return h.store
	}
	return caldav.NewStorage(filepath.Join(h.dataDir, "caldav"))
}

func (h *TaskHandler) sendJSON(w http.ResponseWriter, code int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return
	}
}

func (h *TaskHandler) sendError(w http.ResponseWriter, code int, msg string) {
	h.sendJSON(w, code, map[string]string{"error": msg})
}

// TaskDTO is the JSON projection of a VTODO exchanged with webmail.
type TaskDTO struct {
	UID         string `json:"uid"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	Due         string `json:"due,omitempty"` // RFC3339 or YYYY-MM-DD
	Completed   bool   `json:"completed"`
}

// ensureTaskList returns the dedicated task collection ID, creating it if needed.
func (h *TaskHandler) ensureTaskList(user string) (string, error) {
	store := h.getStorage()
	if _, err := store.GetCalendar(user, taskListID); err == nil {
		return taskListID, nil
	}
	cal := &caldav.Calendar{
		ID:       taskListID,
		Name:     "Tasks",
		Created:  time.Now(),
		Modified: time.Now(),
	}
	if err := store.CreateCalendar(user, cal); err != nil {
		return "", err
	}
	return taskListID, nil
}

// handleTasks lists (GET) or creates (POST) tasks.
func (h *TaskHandler) handleTasks(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	listID, err := h.ensureTaskList(user)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to open task list")
		return
	}
	store := h.getStorage()

	switch r.Method {
	case http.MethodGet:
		raws, err := store.GetEvents(user, listID)
		if err != nil {
			raws = nil
		}
		tasks := make([]TaskDTO, 0, len(raws))
		for _, raw := range raws {
			if dto, ok := parseVTODO(raw); ok {
				tasks = append(tasks, dto)
			}
		}
		h.sendJSON(w, http.StatusOK, map[string]interface{}{"tasks": tasks})
	case http.MethodPost:
		var dto TaskDTO
		if err := decodeJSON(r, &dto); err != nil {
			h.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(dto.Summary) == "" {
			h.sendError(w, http.StatusBadRequest, "summary is required")
			return
		}
		if dto.UID == "" {
			dto.UID = uuid.New().String()
		}
		ics, err := buildVTODO(dto)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, err.Error())
			return
		}
		event := &caldav.CalendarEvent{UID: dto.UID, Summary: dto.Summary, Created: time.Now(), Modified: time.Now()}
		if err := store.SaveEvent(user, listID, event, ics); err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to save task")
			return
		}
		h.sendJSON(w, http.StatusCreated, dto)
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTaskDetail updates (PUT) or deletes (DELETE) one task by UID.
func (h *TaskHandler) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	uid := path.Base(r.URL.Path)
	if uid == "" || uid == "tasks" {
		h.sendError(w, http.StatusBadRequest, "task id required")
		return
	}
	listID, err := h.ensureTaskList(user)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to open task list")
		return
	}
	store := h.getStorage()

	switch r.Method {
	case http.MethodPut:
		var dto TaskDTO
		if err := decodeJSON(r, &dto); err != nil {
			h.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		dto.UID = uid
		if strings.TrimSpace(dto.Summary) == "" {
			h.sendError(w, http.StatusBadRequest, "summary is required")
			return
		}
		ics, err := buildVTODO(dto)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, err.Error())
			return
		}
		event := &caldav.CalendarEvent{UID: uid, Summary: dto.Summary, Modified: time.Now()}
		if err := store.SaveEvent(user, listID, event, ics); err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to save task")
			return
		}
		h.sendJSON(w, http.StatusOK, dto)
	case http.MethodDelete:
		if err := store.DeleteEvent(user, listID, uid); err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to delete task")
			return
		}
		h.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- minimal VTODO parse/generate (reuses the iCalendar helpers in calendar.go) ---

func buildVTODO(dto TaskDTO) (string, error) {
	lines := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//uMailServer//Webmail//EN",
		"BEGIN:VTODO",
		"UID:" + dto.UID,
		"DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z"),
		foldICSLine("SUMMARY:" + icsEscape(dto.Summary)),
	}
	if dto.Due != "" {
		due, err := icsDueValue(dto.Due)
		if err != nil {
			return "", err
		}
		lines = append(lines, due)
	}
	if dto.Description != "" {
		lines = append(lines, foldICSLine("DESCRIPTION:"+icsEscape(dto.Description)))
	}
	if dto.Completed {
		lines = append(lines, "STATUS:COMPLETED", "PERCENT-COMPLETE:100")
	} else {
		lines = append(lines, "STATUS:NEEDS-ACTION")
	}
	lines = append(lines, "END:VTODO", "END:VCALENDAR")
	return strings.Join(lines, "\r\n") + "\r\n", nil
}

// icsDueValue renders a DUE property line. A bare YYYY-MM-DD is treated as an
// all-day due date; otherwise the value is parsed as RFC3339 and emitted in UTC.
func icsDueValue(value string) (string, error) {
	if len(value) == 10 && !strings.Contains(value, "T") {
		t, err := time.Parse("2006-01-02", value)
		if err != nil {
			return "", err
		}
		return "DUE;VALUE=DATE:" + t.Format("20060102"), nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", err
	}
	return "DUE:" + t.UTC().Format("20060102T150405Z"), nil
}

func parseVTODO(ics string) (TaskDTO, bool) {
	var dto TaskDTO
	for _, line := range unfoldICS(ics) {
		name, params, value := splitICSLine(line)
		switch name {
		case "UID":
			dto.UID = value
		case "SUMMARY":
			dto.Summary = icsUnescape(value)
		case "DESCRIPTION":
			dto.Description = icsUnescape(value)
		case "DUE":
			dto.Due, _ = parseICSTime(params, value)
		case "STATUS":
			if strings.EqualFold(value, "COMPLETED") {
				dto.Completed = true
			}
		}
	}
	if dto.UID == "" || dto.Summary == "" {
		return dto, false
	}
	return dto, true
}
