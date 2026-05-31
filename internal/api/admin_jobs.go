package api

import (
	"net/http"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// adminJobDTO mirrors the frontend Job interface (web/admin/src/pages/Jobs.tsx).
type adminJobDTO struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Progress    int    `json:"progress"`
	Mailbox     string `json:"mailbox,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	Error       string `json:"error,omitempty"`
}

// handleAdminJobs handles GET /api/v1/admin/jobs. It lists durable job records
// (migration, backfill, rollback) from the canonical job store. The list is
// empty until a scheduler populates it; no jobs are fabricated.
func (s *Server) handleAdminJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.jobStore == nil {
		// Semantic-core disabled: report an empty, honest list.
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"jobs": []adminJobDTO{}})
		return
	}

	jobs, err := s.jobStore.List("", "")
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list jobs")
		return
	}

	var emails map[string]string
	if s.semStore != nil {
		if m, err := s.semStore.Identity().MailboxEmailsByID(); err == nil {
			emails = m
		}
	}

	out := make([]adminJobDTO, 0, len(jobs))
	for i := range jobs {
		out = append(out, jobToDTO(&jobs[i], emails))
	}
	s.sendJSON(w, http.StatusOK, map[string]interface{}{"jobs": out})
}

func jobToDTO(j *semcore.Job, emails map[string]string) adminJobDTO {
	done, total := j.Progress()
	progress := 0
	if total > 0 {
		progress = done * 100 / total
	}

	mailbox := ""
	if !j.MailboxID.IsZero() {
		if emails != nil {
			mailbox = emails[j.MailboxID.String()]
		}
		if mailbox == "" {
			mailbox = j.MailboxID.String()
		}
	}

	dto := adminJobDTO{
		ID:       j.ID,
		Type:     string(j.Kind),
		Status:   jobStatus(j.State),
		Progress: progress,
		Mailbox:  mailbox,
		Error:    j.LastError,
	}
	if !j.StartedAt.IsZero() {
		dto.StartedAt = j.StartedAt.UTC().Format(time.RFC3339)
	}
	if !j.CompletedAt.IsZero() {
		dto.CompletedAt = j.CompletedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

// jobStatus maps canonical job states onto the four states the admin UI groups
// by. Canceled jobs are surfaced as failed so they appear in job history rather
// than vanishing from both the active and completed lists.
func jobStatus(state semcore.JobState) string {
	switch state {
	case semcore.JobStatePending:
		return "pending"
	case semcore.JobStateRunning:
		return "running"
	case semcore.JobStateCompleted:
		return "completed"
	case semcore.JobStateFailed, semcore.JobStateCanceled:
		return "failed"
	default:
		return "pending"
	}
}
