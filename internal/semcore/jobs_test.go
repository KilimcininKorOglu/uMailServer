package semcore

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Job and JobStep tests
// ---------------------------------------------------------------------------

func TestJobKind_String(t *testing.T) {
	tests := []struct {
		k    JobKind
		want string
	}{
		{JobKindMigration, "migration"},
		{JobKindBackfill, "backfill"},
		{JobKindRollback, "rollback"},
		{JobKind("unknown"), "unknown"},
	}
	for _, tt := range tests {
		if got := string(tt.k); got != tt.want {
			t.Errorf("JobKind(%q).String() = %q, want %q", tt.k, got, tt.want)
		}
	}
}

func TestJobState_String(t *testing.T) {
	tests := []struct {
		s    JobState
		want string
	}{
		{JobStatePending, "pending"},
		{JobStateRunning, "running"},
		{JobStateCompleted, "completed"},
		{JobStateFailed, "failed"},
		{JobStateCanceled, "canceled"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("JobState(%s).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestStepState_String(t *testing.T) {
	tests := []struct {
		s    StepState
		want string
	}{
		{StepStatePending, "pending"},
		{StepStateRunning, "running"},
		{StepStateCompleted, "completed"},
		{StepStateFailed, "failed"},
		{StepStateSkipped, "skipped"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("StepState(%s).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestNewJob(t *testing.T) {
	steps := []JobStep{
		{Name: "step1", Description: "First step", State: StepStatePending},
		{Name: "step2", Description: "Second step", State: StepStatePending},
	}
	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10, steps, "test-actor")

	if job.ID == "" {
		t.Error("NewJob should assign a non-empty ID")
	}
	if job.Kind != JobKindBackfill {
		t.Errorf("Kind = %v, want backfill", job.Kind)
	}
	if job.Target != "item" {
		t.Errorf("Target = %v, want item", job.Target)
	}
	if job.State != JobStatePending {
		t.Errorf("State = %v, want pending", job.State)
	}
	if job.Priority != 10 {
		t.Errorf("Priority = %d, want 10", job.Priority)
	}
	if len(job.Steps) != 2 {
		t.Errorf("Steps len = %d, want 2", len(job.Steps))
	}
	if job.Actor != "test-actor" {
		t.Errorf("Actor = %q, want test-actor", job.Actor)
	}
	if job.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestJob_CurrentStep(t *testing.T) {
	steps := []JobStep{
		{Name: "step1", State: StepStateCompleted},
		{Name: "step2", State: StepStatePending},
		{Name: "step3", State: StepStatePending},
	}
	job := Job{Steps: steps}

	cur := job.CurrentStep()
	if cur == nil {
		t.Fatal("CurrentStep should not be nil")
	}
	if cur.Name != "step2" {
		t.Errorf("current step = %q, want step2", cur.Name)
	}
}

func TestJob_CurrentStep_allDone(t *testing.T) {
	steps := []JobStep{
		{Name: "step1", State: StepStateCompleted},
		{Name: "step2", State: StepStateCompleted},
	}
	job := Job{Steps: steps}
	if job.CurrentStep() != nil {
		t.Error("CurrentStep should be nil when all steps are done")
	}
}

func TestJob_Progress(t *testing.T) {
	steps := []JobStep{
		{Name: "step1", State: StepStateCompleted},
		{Name: "step2", State: StepStateCompleted},
		{Name: "step3", State: StepStatePending},
		{Name: "step4", State: StepStateFailed},
	}
	job := Job{Steps: steps}
	done, total := job.Progress()
	if done != 2 {
		t.Errorf("done = %d, want 2", done)
	}
	if total != 4 {
		t.Errorf("total = %d, want 4", total)
	}
}

func TestJob_IsTerminal(t *testing.T) {
	tests := []struct {
		state JobState
		want  bool
	}{
		{JobStatePending, false},
		{JobStateRunning, false},
		{JobStateCompleted, true},
		{JobStateFailed, true},
		{JobStateCanceled, true},
	}
	for _, tt := range tests {
		job := Job{State: tt.state}
		if got := job.IsTerminal(); got != tt.want {
			t.Errorf("Job{State=%s}.IsTerminal() = %v, want %v", tt.state, got, tt.want)
		}
	}
}

func TestJob_EncodeResumeCursor(t *testing.T) {
	steps := []JobStep{
		{Name: "step1", State: StepStateCompleted},
		{Name: "step2", State: StepStatePending},
		{Name: "step3", State: StepStatePending},
	}
	job := Job{Steps: steps}
	job.EncodeResumeCursor()

	if job.Cursor == "" || job.Cursor == "done" {
		t.Errorf("Cursor = %q, want step=1", job.Cursor)
	}
}

func TestJob_EncodeResumeCursor_allDone(t *testing.T) {
	steps := []JobStep{
		{Name: "step1", State: StepStateCompleted},
		{Name: "step2", State: StepStateCompleted},
	}
	job := Job{Steps: steps}
	job.EncodeResumeCursor()
	if job.Cursor != "done" {
		t.Errorf("Cursor = %q, want done", job.Cursor)
	}
}

func TestJob_DecodeResumeStep(t *testing.T) {
	tests := []struct {
		cursor   string
		wantStep int
	}{
		{"step=3", 3},
		{"step=0", 0},
		{"step=99", 99},
		{"", 0},
		{"done", 0},
		{"invalid", 0},
	}
	for _, tt := range tests {
		job := Job{Cursor: tt.cursor}
		got := job.DecodeResumeStep()
		if got != tt.wantStep {
			t.Errorf("DecodeResumeStep(%q) = %d, want %d", tt.cursor, got, tt.wantStep)
		}
	}
}

func TestJob_Marshal_Unmarshal(t *testing.T) {
	steps := []JobStep{
		{Name: "step1", State: StepStateCompleted, Checkpoint: "uid=100"},
		{Name: "step2", State: StepStatePending},
	}
	job := Job{
		ID:        "job-123",
		Kind:      JobKindBackfill,
		Target:    "item",
		State:     JobStateRunning,
		Priority:  10,
		Steps:     steps,
		Cursor:    "step=0",
		Errors:    0,
		CreatedAt: time.Now(),
		StartedAt: time.Now(),
	}

	data, err := job.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}

	restored, err := UnmarshalJob(data)
	if err != nil {
		t.Fatalf("UnmarshalJob error: %v", err)
	}

	if restored.ID != job.ID {
		t.Errorf("ID = %q, want %q", restored.ID, job.ID)
	}
	if restored.Kind != job.Kind {
		t.Errorf("Kind = %v, want %v", restored.Kind, job.Kind)
	}
	if restored.State != job.State {
		t.Errorf("State = %v, want %v", restored.State, job.State)
	}
	if len(restored.Steps) != len(job.Steps) {
		t.Errorf("Steps len = %d, want %d", len(restored.Steps), len(job.Steps))
	}
	if restored.Steps[0].Checkpoint != "uid=100" {
		t.Errorf("step1 checkpoint = %q, want uid=100", restored.Steps[0].Checkpoint)
	}
	if restored.Cursor != "step=0" {
		t.Errorf("Cursor = %q, want step=0", restored.Cursor)
	}
}

func TestErrJobNotFound(t *testing.T) {
	if ErrJobNotFound == nil {
		t.Error("ErrJobNotFound should not be nil")
	}
	if ErrJobNotFound.Error() == "" {
		t.Error("ErrJobNotFound.Error() should be non-empty")
	}
}
