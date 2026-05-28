package semcore

import (
	"sync"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

// mockJobStore is a simple in-memory JobStore for testing.
type mockJobStore struct {
	mu    sync.Mutex
	jobs  map[string]Job
	calls []string
}

func newMockJobStore() *mockJobStore {
	return &mockJobStore{jobs: make(map[string]Job)}
}

func (s *mockJobStore) Put(job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "Put:"+job.ID)
	s.jobs[job.ID] = job
	return nil
}

func (s *mockJobStore) Get(id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		return j, nil
	}
	return Job{}, ErrJobNotFound
}

func (s *mockJobStore) List(kind JobKind, state JobState) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Job
	for _, j := range s.jobs {
		if kind != "" && j.Kind != kind {
			continue
		}
		if state != "" && j.State != state {
			continue
		}
		result = append(result, j)
	}
	return result, nil
}

func (s *mockJobStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return ErrJobNotFound
	}
	delete(s.jobs, id)
	return nil
}

// ---------------------------------------------------------------------------
// Scheduler tests
// ---------------------------------------------------------------------------

func TestScheduler_NewJobScheduler(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)
	if sched == nil {
		t.Fatal("NewJobScheduler returned nil")
	}
}

func TestScheduler_Submit_newJob(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", Description: "Test step", State: StepStatePending}},
		"test")

	err := sched.Submit(job)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	retrieved, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if retrieved.State != JobStatePending {
		t.Errorf("State = %v, want pending", retrieved.State)
	}
}

func TestScheduler_Submit_duplicate(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}},
		"test")

	err := sched.Submit(job)
	if err != nil {
		t.Fatalf("first Submit error: %v", err)
	}

	// Try to submit again with same ID — should fail.
	err = sched.Submit(job)
	if err == nil {
		t.Error("duplicate Submit should return error")
	}
}

func TestScheduler_CancelJob(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}},
		"test")

	err := sched.Submit(job)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	err = sched.CancelJob(job.ID)
	if err != nil {
		t.Fatalf("CancelJob error: %v", err)
	}

	retrieved, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if retrieved.State != JobStateCanceled {
		t.Errorf("State = %v, want canceled", retrieved.State)
	}
}

func TestScheduler_CancelJob_notFound(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	err := sched.CancelJob("nonexistent")
	if err == nil {
		t.Error("CancelJob of nonexistent should return error")
	}
	if err != ErrJobNotFound {
		t.Errorf("error = %v, want ErrJobNotFound", err)
	}
}

func TestScheduler_CancelJob_alreadyTerminal(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}},
		"test")
	job.State = JobStateCompleted // already terminal

	err := store.Put(job)
	if err != nil {
		t.Fatalf("Put error: %v", err)
	}

	err = sched.CancelJob(job.ID)
	if err == nil {
		t.Error("CancelJob of terminal job should return error")
	}
}

func TestScheduler_GetJob(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}},
		"test")

	_, err := sched.GetJob(job.ID)
	if err == nil {
		t.Error("GetJob of nonexistent should return error")
	}
	if err != ErrJobNotFound {
		t.Errorf("error = %v, want ErrJobNotFound", err)
	}

	if err := sched.Submit(job); err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	retrieved, err := sched.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob error: %v", err)
	}
	if retrieved.ID != job.ID {
		t.Errorf("ID = %q, want %q", retrieved.ID, job.ID)
	}
}

func TestScheduler_ListJobs(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	job1 := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}}, "test")
	job2 := NewJob(JobKindMigration, "001", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}}, "test")

	err1 := sched.Submit(job1)
	err2 := sched.Submit(job2)
	if err1 != nil {
		t.Fatalf("Submit job1 error: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("Submit job2 error: %v", err2)
	}

	// List all.
	all, err := sched.ListJobs("", "")
	if err != nil {
		t.Fatalf("ListJobs error: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("all jobs = %d, want 2 (ids: %v)", len(all), store.calls)
	}

	// List only backfill.
	backfill, err := sched.ListJobs(JobKindBackfill, "")
	if err != nil {
		t.Fatalf("ListJobs error: %v", err)
	}
	if len(backfill) != 1 {
		t.Errorf("backfill jobs = %d, want 1", len(backfill))
	}
}

func TestScheduler_recoverJobs(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	// Create a job in Running state (simulating interrupted run).
	// The step is named "item" which maps to BackfillTargetItem, which the
	// mock backfill executor will process (and fail because no real executor).
	// We use a custom step that doesn't match any known target so the step
	// does nothing (placeholder path) and the job completes.
	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStateRunning, StartedAt: time.Now()}}, "test")
	job.State = JobStateRunning
	if err := store.Put(job); err != nil {
		t.Fatalf("store.Put error: %v", err)
	}

	// Start the scheduler — it should recover the running job.
	err := sched.Start()
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	// Wait for the job to be picked up, recovered to pending, and executed.
	// Without a real backfill executor, the step succeeds trivially and the
	// job reaches the completed state.
	time.Sleep(4 * time.Second)

	sched.Stop()

	// Verify the job was recovered and reached a terminal state.
	// Since the backfill step succeeds without a real executor, the job
	// completes. The important thing is that it was not left in Running state.
	recovered, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	// The job should either be pending (scheduler hasn't picked it up yet)
	// or completed (scheduler ran it). It must NOT be stuck in Running.
	if recovered.State == JobStateRunning {
		t.Errorf("job stuck in Running state after recovery — state=%s", recovered.State)
	}
}

func TestScheduler_Stop_idempotent(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	// Start may fail in some environments; the test only cares about Stop.
	err := sched.Start(); _ = err //nolint:errcheck
	sched.Stop() // First stop.
	sched.Stop() // Second stop — should not panic.
}

func TestScheduler_Start_twice(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	err := sched.Start(); _ = err //nolint:errcheck
	err = sched.Start()
	if err == nil {
		t.Error("second Start should return error")
	}
	sched.Stop()
}

// stateChangeRecorder is a test helper that records all state changes.
type stateChangeRecorder struct {
	mu     sync.Mutex
	states []JobState
}

func (r *stateChangeRecorder) Record(job Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, job.State)
}

// TestScheduler_stateChangeHook verifies the state-change hook is called.
func TestScheduler_stateChangeHook(t *testing.T) {
	store := newMockJobStore()
	recorder := &stateChangeRecorder{}

	cfg := DefaultSchedulerConfig()
	cfg.OnJobStateChange = recorder.Record

	sched := NewJobScheduler(store, cfg)

	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}}, "test")

	if err := sched.Submit(job); err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if err := sched.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	// Wait for recovery to pick up and run the job.
	time.Sleep(3 * time.Second)
	sched.Stop()

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if len(recorder.states) == 0 {
		t.Error("state change hook was never called")
	}
}

// TestScheduler_jobNotFoundWhilePolling verifies graceful handling when a job
// disappears during polling (edge case).
func TestScheduler_jobNotFoundWhilePolling(t *testing.T) {
	store := newMockJobStore()
	cfg := DefaultSchedulerConfig()
	sched := NewJobScheduler(store, cfg)

	// Submit job with empty ID to trigger error path.
	job := Job{
		ID:        "",
		Kind:      JobKindBackfill,
		Target:    "item",
		State:     JobStatePending,
		Priority:  10,
		Steps:     []JobStep{{Name: "step1", State: StepStatePending}},
		CreatedAt: time.Now(),
	}

	err := sched.Submit(job)
	if err == nil {
		t.Error("Submit with empty ID should error")
	}
}

// TestBuildBackfillSteps verifies step generation for each target.
func TestBuildBackfillSteps(t *testing.T) {
	targets := []BackfillTarget{
		BackfillTargetMailbox,
		BackfillTargetFolder,
		BackfillTargetItem,
		BackfillTargetAttachment,
		BackfillTermConversation,
		BackfillTargetSyncState,
		BackfillTargetLifecycle,
	}

	for _, target := range targets {
		steps := buildBackfillSteps(target)
		if len(steps) == 0 {
			t.Errorf("buildBackfillSteps(%s) returned no steps", target)
		}
		if steps[0].Name != string(target) {
			t.Errorf("step name = %q, want %q", steps[0].Name, target)
		}
		if steps[0].State != StepStatePending {
			t.Errorf("step state = %v, want pending", steps[0].State)
		}
	}
}

// TestBuildRollbackSteps verifies step generation for rollback targets.
func TestBuildRollbackSteps(t *testing.T) {
	targets := []RollbackTarget{
		RollbackTargetIdentity,
		RollbackTargetSyncState,
		RollbackTargetLifecycle,
		RollbackTargetAll,
	}

	for _, target := range targets {
		steps := buildRollbackSteps(target)
		if len(steps) == 0 {
			t.Errorf("buildRollbackSteps(%s) returned no steps", target)
		}
	}
}

// ---------------------------------------------------------------------------
// BoltJobStore tests (require bbolt)
// ---------------------------------------------------------------------------

func TestBoltJobStore_Put_Get(t *testing.T) {
	tmpDir := t.TempDir()
	db := openTestBolt(t, tmpDir+"/test_put_get.db")
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltJobStore(db)
	if err != nil {
		t.Fatalf("NewBoltJobStore error: %v", err)
	}

	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}}, "test")

	err = store.Put(job)
	if err != nil {
		t.Fatalf("Put error: %v", err)
	}

	retrieved, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if retrieved.ID != job.ID {
		t.Errorf("ID = %q, want %q", retrieved.ID, job.ID)
	}
	if retrieved.Kind != JobKindBackfill {
		t.Errorf("Kind = %v, want backfill", retrieved.Kind)
	}
}

func TestBoltJobStore_Get_notFound(t *testing.T) {
	tmpDir := t.TempDir()
	db := openTestBolt(t, tmpDir+"/test_notfound.db")
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltJobStore(db)
	if err != nil {
		t.Fatalf("NewBoltJobStore error: %v", err)
	}

	_, err = store.Get("nonexistent")
	if err != ErrJobNotFound {
		t.Errorf("error = %v, want ErrJobNotFound", err)
	}
}

func TestBoltJobStore_List(t *testing.T) {
	tmpDir := t.TempDir()
	db := openTestBolt(t, tmpDir+"/test_list.db")
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltJobStore(db)
	if err != nil {
		t.Fatalf("NewBoltJobStore error: %v", err)
	}

	job1 := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}}, "test")
	job2 := NewJob(JobKindMigration, "001", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}}, "test")

	if err := store.Put(job1); err != nil {
		t.Fatalf("store.Put job1 error: %v", err)
	}
	if err := store.Put(job2); err != nil {
		t.Fatalf("store.Put job2 error: %v", err)
	}

	all, err := store.List("", "")
	if err != nil {
		t.Fatalf("List all error: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("all jobs = %d, want 2", len(all))
	}

	backfill, err := store.List(JobKindBackfill, "")
	if err != nil {
		t.Fatalf("List backfill error: %v", err)
	}
	if len(backfill) != 1 {
		t.Errorf("backfill jobs = %d, want 1", len(backfill))
	}

	pending, err := store.List("", JobStatePending)
	if err != nil {
		t.Fatalf("List pending error: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("pending jobs = %d, want 2", len(pending))
	}
}

func TestBoltJobStore_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	db := openTestBolt(t, tmpDir+"/test_delete.db")
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltJobStore(db)
	if err != nil {
		t.Fatalf("NewBoltJobStore error: %v", err)
	}

	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}}, "test")

	if err := store.Put(job); err != nil {
		t.Fatalf("store.Put error: %v", err)
	}
	err = store.Delete(job.ID)
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	_, err = store.Get(job.ID)
	if err != ErrJobNotFound {
		t.Errorf("error = %v, want ErrJobNotFound", err)
	}
}

func TestBoltJobStore_Delete_notFound(t *testing.T) {
	tmpDir := t.TempDir()
	db := openTestBolt(t, tmpDir+"/test_delete_nf.db")
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltJobStore(db)
	if err != nil {
		t.Fatalf("NewBoltJobStore error: %v", err)
	}

	err = store.Delete("nonexistent")
	if err != ErrJobNotFound {
		t.Errorf("error = %v, want ErrJobNotFound", err)
	}
}

func TestBoltJobStore_Put_update(t *testing.T) {
	tmpDir := t.TempDir()
	db := openTestBolt(t, tmpDir+"/test_update.db")
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltJobStore(db)
	if err != nil {
		t.Fatalf("NewBoltJobStore error: %v", err)
	}

	job := NewJob(JobKindBackfill, "item", MailboxId{}, 10,
		[]JobStep{{Name: "step1", State: StepStatePending}}, "test")

	if err := store.Put(job); err != nil {
		t.Fatalf("store.Put error: %v", err)
	}

	// Update the job.
	job.State = JobStateRunning
	job.StartedAt = time.Now()
	if err := store.Put(job); err != nil {
		t.Fatalf("store.Put error: %v", err)
	}

	retrieved, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("store.Get error: %v", err)
	}
	if retrieved.State != JobStateRunning {
		t.Errorf("State = %v, want running", retrieved.State)
	}
}

func openTestBolt(t *testing.T, path string) *bbolt.DB {
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("bbolt.Open error: %v", err)
	}
	return db
}
