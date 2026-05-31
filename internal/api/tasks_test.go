package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVTODO_RoundTrip(t *testing.T) {
	in := TaskDTO{UID: "t1", Summary: "Ship release; tag it", Description: "notes\nhere", Due: "2026-06-09", Completed: true}
	ics, err := buildVTODO(in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(ics, "BEGIN:VTODO") || !strings.Contains(ics, "STATUS:COMPLETED") {
		t.Fatalf("expected VTODO completed, got:\n%s", ics)
	}
	out, ok := parseVTODO(ics)
	if !ok {
		t.Fatalf("parse failed:\n%s", ics)
	}
	if out.Summary != in.Summary || out.Description != in.Description || out.Due != in.Due || !out.Completed {
		t.Errorf("round-trip mismatch: got %+v", out)
	}
}

func TestTaskHandler_CRUD(t *testing.T) {
	h := NewTaskHandler(t.TempDir())

	// Create.
	rec := httptest.NewRecorder()
	h.handleTasks(rec, reqAsUser(http.MethodPost, "/api/v1/tasks", `{"summary":"Write docs","due":"2026-06-12","completed":false}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created TaskDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.UID == "" {
		t.Fatal("expected generated UID")
	}

	// List.
	rec = httptest.NewRecorder()
	h.handleTasks(rec, reqAsUser(http.MethodGet, "/api/v1/tasks", ""))
	if !strings.Contains(rec.Body.String(), "Write docs") {
		t.Fatalf("expected task in list, got %s", rec.Body.String())
	}

	// Mark completed via update.
	rec = httptest.NewRecorder()
	h.handleTaskDetail(rec, reqAsUser(http.MethodPut, "/api/v1/tasks/"+created.UID, `{"summary":"Write docs","completed":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.handleTasks(rec, reqAsUser(http.MethodGet, "/api/v1/tasks", ""))
	var listResp struct {
		Tasks []TaskDTO `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Tasks) != 1 || !listResp.Tasks[0].Completed {
		t.Fatalf("expected one completed task, got %+v", listResp.Tasks)
	}

	// Delete.
	rec = httptest.NewRecorder()
	h.handleTaskDetail(rec, reqAsUser(http.MethodDelete, "/api/v1/tasks/"+created.UID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", rec.Code)
	}
}
