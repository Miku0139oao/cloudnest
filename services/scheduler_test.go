package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloudnest/internal/dto"
	"cloudnest/internal/engine"
	"cloudnest/internal/rc"
	"cloudnest/internal/settings"
)

// schedulerTestEngine serves a fixed rc.Client for the liveness probes the
// overlap guard makes. status is returned by Status so detach/restart paths can
// see a ready engine when a test needs one.
type schedulerTestEngine struct {
	engine.Engine
	client    *rc.Client
	clientErr error
	status    engine.Status
	calls     int
}

func (e *schedulerTestEngine) Client() (*rc.Client, error) {
	e.calls++
	if e.clientErr != nil {
		return nil, e.clientErr
	}
	return e.client, nil
}

func (e *schedulerTestEngine) Status() engine.Status { return e.status }

// schedulerRC starts a fake rclone RC server. status answers job/status and
// start answers sync/copy; a nil start replies with a successful job id. The
// returned counter counts sync/copy requests, which is how the tests tell a
// scheduled StartBackup apart from the liveness probe.
func schedulerRC(t *testing.T, status func(id int64) (int, string), start func(http.ResponseWriter)) (*rc.Client, *int32) {
	t.Helper()
	var starts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/job/status":
			var in struct {
				JobID int64 `json:"jobid"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			code, body := http.StatusOK, `{"id":11,"finished":false}`
			if status != nil {
				code, body = status(in.JobID)
			}
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		case "/sync/copy":
			atomic.AddInt32(&starts, 1)
			if start != nil {
				start(w)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 21})
		case "/core/stats":
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return rc.New(server.URL, "", "", nil), &starts
}

func unfinishedJob(int64) (int, string) {
	return http.StatusOK, `{"id":11,"finished":false}`
}

// newSchedulerTestService builds the smallest Service the scheduler touches:
// settings for the schedule, a fake rclone and the job map the overlap guard
// inspects.
func newSchedulerTestService(t *testing.T, src string, client *rc.Client) (*Service, *schedulerTestEngine) {
	t.Helper()
	store, err := settings.New(t.TempDir())
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}
	if err := store.Update(func(cur *dto.Settings) {
		cur.Schedule = dto.Schedule{
			Enabled:     true,
			SourcePaths: []string{src},
			AccountID:   "gdrive",
			DestRoot:    "電腦備份",
			Time:        "00:00", // already passed today, so the point is due
		}
	}); err != nil {
		t.Fatalf("store.Update: %v", err)
	}
	eng := &schedulerTestEngine{client: client, status: engine.Status{State: engine.StateReady}}
	return &Service{store: store, eng: eng, jobs: make(map[string]*jobState)}, eng
}

func TestSchedulerSkipsOverlappingBackup(t *testing.T) {
	src := t.TempDir()

	tests := []struct {
		name      string
		jobState  string
		kind      string
		accountID string
		dest      string
		sources   []string
		wantStart bool
	}{
		{
			name: "running backup of the same sources", jobState: "running",
			kind: "backup", accountID: "gdrive", dest: "電腦備份",
			sources: []string{src}, wantStart: false,
		},
		{
			name: "paused backup of the same sources", jobState: "paused",
			kind: "backup", accountID: "gdrive", dest: "電腦備份",
			sources: []string{src}, wantStart: false,
		},
		{
			name: "one shared source is enough", jobState: "running",
			kind: "backup", accountID: "gdrive", dest: "電腦備份",
			sources: []string{t.TempDir(), src}, wantStart: false,
		},
		{
			name: "finished backup does not block", jobState: "done",
			kind: "backup", accountID: "gdrive", dest: "電腦備份",
			sources: []string{src}, wantStart: true,
		},
		{
			name: "different destination does not block", jobState: "running",
			kind: "backup", accountID: "gdrive", dest: "其他備份",
			sources: []string{src}, wantStart: true,
		},
		{
			name: "different account does not block", jobState: "running",
			kind: "backup", accountID: "onedrive", dest: "電腦備份",
			sources: []string{src}, wantStart: true,
		},
		{
			name: "different source does not block", jobState: "running",
			kind: "backup", accountID: "gdrive", dest: "電腦備份",
			sources: []string{t.TempDir()}, wantStart: true,
		},
		{
			name: "non-backup job does not block", jobState: "running",
			kind: "restore", accountID: "gdrive", dest: "電腦備份",
			sources: []string{src}, wantStart: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, starts := schedulerRC(t, unfinishedJob, nil)
			s, _ := newSchedulerTestService(t, src, client)
			s.jobs["job-existing"] = &jobState{
				ID:        "job-existing",
				Kind:      tc.kind,
				State:     tc.jobState,
				AccountID: tc.accountID,
				DestRoot:  tc.dest,
				Sources:   tc.sources,
				JobIDs:    []int64{11},
			}
			state := &schedulerState{}

			s.evaluateSchedule(state)

			started := atomic.LoadInt32(starts) > 0
			if started != tc.wantStart {
				t.Fatalf("backup start attempted = %v (sync/copy calls %d), want %v",
					started, atomic.LoadInt32(starts), tc.wantStart)
			}
			if !tc.wantStart {
				if state.lastPoint != 0 {
					t.Fatalf("skipped point was marked as attempted: lastPoint = %d", state.lastPoint)
				}
				if state.blockedJob != "job-existing" {
					t.Fatalf("blockedJob = %q, want job-existing", state.blockedJob)
				}
				s.mu.Lock()
				n := len(s.jobs)
				s.mu.Unlock()
				if n != 1 {
					t.Fatalf("job map grew to %d entries; no job should be added", n)
				}
			}
		})
	}

	if runtime.GOOS == "windows" {
		t.Run("source case does not matter on windows", func(t *testing.T) {
			client, starts := schedulerRC(t, unfinishedJob, nil)
			s, _ := newSchedulerTestService(t, src, client)
			s.jobs["job-existing"] = &jobState{
				ID: "job-existing", Kind: "backup", State: "running",
				AccountID: "gdrive", DestRoot: "電腦備份",
				Sources: []string{strings.ToUpper(src)}, JobIDs: []int64{11},
			}
			s.evaluateSchedule(&schedulerState{})
			if got := atomic.LoadInt32(starts); got != 0 {
				t.Fatalf("sync/copy calls = %d, want 0 for a case-insensitive source match", got)
			}
		})
	}
}

// TestSchedulerReconcilesFinishedJob proves a backup which finished while
// nothing was polling ActiveJobs no longer blocks the next scheduled point.
func TestSchedulerReconcilesFinishedJob(t *testing.T) {
	src := t.TempDir()
	client, starts := schedulerRC(t, func(int64) (int, string) {
		return http.StatusOK, `{"id":11,"finished":true,"success":true}`
	}, nil)
	s, _ := newSchedulerTestService(t, src, client)
	s.jobs["job-old"] = &jobState{
		ID: "job-old", Group: "gui-job-old", Kind: "backup", State: "running",
		AccountID: "gdrive", DestRoot: "電腦備份", Sources: []string{src},
		JobIDs: []int64{11}, StartedAt: time.Now(),
	}

	state := &schedulerState{}
	s.evaluateSchedule(state)

	s.mu.Lock()
	oldState := s.jobs["job-old"].State
	records := append([]dto.Record(nil), s.records...)
	s.mu.Unlock()
	if oldState != "done" {
		t.Fatalf("stale job state = %q, want done after reconciliation", oldState)
	}
	if len(records) != 1 || records[0].ID != "job-old" || records[0].State != "done" {
		t.Fatalf("history = %+v, want one done record for job-old", records)
	}
	if got := atomic.LoadInt32(starts); got != 1 {
		t.Fatalf("sync/copy calls = %d, want 1 (the scheduled backup must start)", got)
	}
	if state.lastPoint == 0 {
		t.Fatal("point was not marked as attempted after the backup started")
	}
}

// TestSchedulerReconcilesJobWhoseIdsAreGone covers an engine restart: the job
// started before the current engine became ready and its ids no longer exist.
// The run ended but may have completed before the restart, so it is recorded as
// interrupted rather than failed, and the next point is not blocked.
func TestSchedulerReconcilesJobWhoseIdsAreGone(t *testing.T) {
	src := t.TempDir()
	client, starts := schedulerRC(t, func(int64) (int, string) {
		return http.StatusInternalServerError, `{"error":"job not found","status":500}`
	}, nil)
	s, eng := newSchedulerTestService(t, src, client)
	// The engine became ready after the job started, so the ids belong to the
	// previous process and were lost.
	eng.status = engine.Status{State: engine.StateReady, Since: time.Now().Add(-time.Minute)}
	s.jobs["job-gone"] = &jobState{
		ID: "job-gone", Group: "gui-job-gone", Kind: "backup", State: "running",
		AccountID: "gdrive", DestRoot: "電腦備份", Sources: []string{src},
		JobIDs: []int64{77}, StartedAt: time.Now().Add(-time.Hour),
	}

	state := &schedulerState{}
	s.evaluateSchedule(state)

	s.mu.Lock()
	j := s.jobs["job-gone"]
	st, msg := j.State, j.Err
	s.mu.Unlock()
	if st != "interrupted" {
		t.Fatalf("job state = %q, want interrupted: a restart does not prove the run failed", st)
	}
	if !strings.Contains(msg, "may have completed") {
		t.Fatalf("job error = %q, want a clear unobserved-outcome explanation", msg)
	}
	if got := atomic.LoadInt32(starts); got != 1 {
		t.Fatalf("sync/copy calls = %d, want 1 after the stale job was reconciled", got)
	}
	if state.lastPoint == 0 {
		t.Fatal("point was not marked as attempted after the backup started")
	}
}

// TestSchedulerReconcilesExpiredJobAsInterrupted covers the tray scenario: the
// backup finished while nothing polled ActiveJobs, rclone purged its ids about a
// minute later, and the next scheduled point must reconcile it rather than write
// a false success or a false failure. The outcome was never observed, so the
// record is interrupted and the point is unblocked.
func TestSchedulerReconcilesExpiredJobAsInterrupted(t *testing.T) {
	src := t.TempDir()
	client, starts := schedulerRC(t, func(int64) (int, string) {
		return http.StatusInternalServerError, `{"error":"job not found","status":500}`
	}, nil)
	s, eng := newSchedulerTestService(t, src, client)
	// Same engine: the job started after this process became ready.
	eng.status = engine.Status{State: engine.StateReady, Since: time.Now().Add(-time.Hour)}
	s.jobs["job-expired"] = &jobState{
		ID: "job-expired", Group: "gui-job-expired", Kind: "backup", State: "running",
		AccountID: "gdrive", DestRoot: "電腦備份", Sources: []string{src},
		JobIDs: []int64{11}, StartedAt: time.Now().Add(-30 * time.Minute),
	}

	state := &schedulerState{}
	s.evaluateSchedule(state)

	s.mu.Lock()
	j := s.jobs["job-expired"]
	st, msg := j.State, j.Err
	records := append([]dto.Record(nil), s.records...)
	s.mu.Unlock()
	if st != "interrupted" {
		t.Fatalf("job state = %q (%q), want interrupted: an expired id does not prove success", st, msg)
	}
	if msg == "" {
		t.Fatal("interrupted job has no explanation of the unobserved outcome")
	}
	if len(records) != 1 || records[0].ID != "job-expired" || records[0].State != "interrupted" {
		t.Fatalf("history = %+v, want one interrupted record for job-expired", records)
	}
	if records[0].Error == "" {
		t.Fatal("interrupted history record has no explanation")
	}
	if got := atomic.LoadInt32(starts); got != 1 {
		t.Fatalf("sync/copy calls = %d, want 1 after the finished job was reconciled", got)
	}
	if state.lastPoint == 0 {
		t.Fatal("point was not marked as attempted after the backup started")
	}
}

// TestSchedulerReconcilesFinishedFailure covers a definitive rclone failure:
// unlike an expired id, it is preserved as failed with rclone's message.
func TestSchedulerReconcilesFinishedFailure(t *testing.T) {
	src := t.TempDir()
	client, starts := schedulerRC(t, func(int64) (int, string) {
		return http.StatusOK, `{"id":11,"finished":true,"success":false,"error":"quota exceeded"}`
	}, nil)
	s, _ := newSchedulerTestService(t, src, client)
	s.jobs["job-failed"] = &jobState{
		ID: "job-failed", Group: "gui-job-failed", Kind: "backup", State: "running",
		AccountID: "gdrive", DestRoot: "電腦備份", Sources: []string{src},
		JobIDs: []int64{11}, StartedAt: time.Now(),
	}

	s.evaluateSchedule(&schedulerState{})

	s.mu.Lock()
	st, msg := s.jobs["job-failed"].State, s.jobs["job-failed"].Err
	s.mu.Unlock()
	if st != "failed" {
		t.Fatalf("job state = %q, want failed", st)
	}
	if !strings.Contains(msg, "quota exceeded") {
		t.Fatalf("job error = %q, want rclone's failure message", msg)
	}
	if got := atomic.LoadInt32(starts); got != 1 {
		t.Fatalf("sync/copy calls = %d, want 1 after the run was reconciled", got)
	}
}

// TestSchedulerProbeErrorKeepsJobBusy pins the corrected semantics: an rclone
// error which is not "job not found" says nothing about liveness, so the run
// stays busy instead of being failed. Classifying it as gone used to let a
// second backup start over a live transfer.
func TestSchedulerProbeErrorKeepsJobBusy(t *testing.T) {
	src := t.TempDir()
	client, starts := schedulerRC(t, func(int64) (int, string) {
		return http.StatusInternalServerError, `{"error":"internal error","status":500}`
	}, nil)
	s, _ := newSchedulerTestService(t, src, client)
	s.jobs["job-live"] = &jobState{
		ID: "job-live", Group: "gui-job-live", Kind: "backup", State: "running",
		AccountID: "gdrive", DestRoot: "電腦備份", Sources: []string{src},
		JobIDs: []int64{11}, StartedAt: time.Now(),
	}

	state := &schedulerState{}
	s.evaluateSchedule(state)

	s.mu.Lock()
	st := s.jobs["job-live"].State
	records := len(s.records)
	s.mu.Unlock()
	if st != "running" {
		t.Fatalf("job state = %q, want running: an unanswerable probe is not proof the job is gone", st)
	}
	if records != 0 {
		t.Fatalf("history records = %d, want 0", records)
	}
	if got := atomic.LoadInt32(starts); got != 0 {
		t.Fatalf("sync/copy calls = %d, want 0 (a probe error must not start a duplicate)", got)
	}
	if state.blockedJob != "job-live" {
		t.Fatalf("blockedJob = %q, want job-live", state.blockedJob)
	}
}

// TestSchedulerProbeTimeoutKeepsJobBusy covers the deadline branch specifically:
// the engine does not answer within schedulerProbeTimeout, and that must be
// treated as busy, not as a lost job.
func TestSchedulerProbeTimeoutKeepsJobBusy(t *testing.T) {
	old := schedulerProbeTimeout
	schedulerProbeTimeout = 50 * time.Millisecond
	defer func() { schedulerProbeTimeout = old }()

	src := t.TempDir()
	client, starts := schedulerRC(t, func(int64) (int, string) {
		time.Sleep(500 * time.Millisecond)
		return http.StatusOK, `{"id":11,"finished":false}`
	}, nil)
	s, _ := newSchedulerTestService(t, src, client)
	s.jobs["job-slow"] = &jobState{
		ID: "job-slow", Group: "gui-job-slow", Kind: "backup", State: "running",
		AccountID: "gdrive", DestRoot: "電腦備份", Sources: []string{src},
		JobIDs: []int64{11}, StartedAt: time.Now(),
	}

	state := &schedulerState{}
	s.evaluateSchedule(state)

	s.mu.Lock()
	st := s.jobs["job-slow"].State
	s.mu.Unlock()
	if st != "running" {
		t.Fatalf("job state = %q, want running after a probe timeout", st)
	}
	if got := atomic.LoadInt32(starts); got != 0 {
		t.Fatalf("sync/copy calls = %d, want 0 (a probe timeout must not start a duplicate)", got)
	}
	if state.blockedJob != "job-slow" {
		t.Fatalf("blockedJob = %q, want job-slow", state.blockedJob)
	}
}

// TestSchedulerSplitRunningJobBlocks proves the split goroutine's liveness flag
// holds the point without any rclone probe.
func TestSchedulerSplitRunningJobBlocks(t *testing.T) {
	src := t.TempDir()
	client, starts := schedulerRC(t, unfinishedJob, nil)
	s, eng := newSchedulerTestService(t, src, client)
	s.jobs["job-split"] = &jobState{
		ID: "job-split", Kind: "backup", State: "running", SplitRunning: true,
		AccountID: "gdrive", DestRoot: "電腦備份", Sources: []string{src},
		JobIDs: []int64{pendingSplitJobID},
	}

	state := &schedulerState{}
	s.evaluateSchedule(state)

	if got := atomic.LoadInt32(starts); got != 0 {
		t.Fatalf("sync/copy calls = %d, want 0 while the split goroutine is running", got)
	}
	if eng.calls != 0 {
		t.Fatalf("engine probes = %d, want 0 (SplitRunning is authoritative)", eng.calls)
	}
	if state.blockedJob != "job-split" {
		t.Fatalf("blockedJob = %q, want job-split", state.blockedJob)
	}
}

// TestSchedulerResumingJobBlocksWithoutProbe covers finding 1 on the scheduler
// side: while ResumeJob reserves a job its cached ids belong to the stopped run,
// so the overlap guard must treat it as in flight without probing (a probe would
// terminalise it and let a duplicate backup start).
func TestSchedulerResumingJobBlocksWithoutProbe(t *testing.T) {
	src := t.TempDir()
	client, starts := schedulerRC(t, func(int64) (int, string) {
		// If this were consulted it would say the paused run's ids are gone,
		// which must not terminalise the reserved job.
		return http.StatusInternalServerError, `{"error":"job not found","status":500}`
	}, nil)
	s, eng := newSchedulerTestService(t, src, client)
	s.jobs["job-resuming"] = &jobState{
		ID: "job-resuming", Group: "gui-job-resuming", Kind: "backup", State: "running",
		Resuming: true, AccountID: "gdrive", DestRoot: "電腦備份", Sources: []string{src},
		JobIDs: []int64{11}, StartedAt: time.Now().Add(-time.Minute),
	}

	state := &schedulerState{}
	s.evaluateSchedule(state)

	s.mu.Lock()
	st := s.jobs["job-resuming"].State
	records := len(s.records)
	s.mu.Unlock()
	if st != "running" {
		t.Fatalf("job state = %q, want running: a reserved resume must not be terminalised", st)
	}
	if records != 0 {
		t.Fatalf("history records = %d, want 0", records)
	}
	if got := atomic.LoadInt32(starts); got != 0 {
		t.Fatalf("sync/copy calls = %d, want 0 (no duplicate backup during a resume)", got)
	}
	if eng.calls != 0 {
		t.Fatalf("engine probes = %d, want 0 (Resuming is authoritative)", eng.calls)
	}
	if state.blockedJob != "job-resuming" {
		t.Fatalf("blockedJob = %q, want job-resuming", state.blockedJob)
	}
}

// TestSchedulerPausedBackupRecordsVisibleBlock proves a paused backup holding
// a scheduled point is surfaced in History exactly once per point, instead of
// only living in a log line while automatic backups are silently skipped.
func TestSchedulerPausedBackupRecordsVisibleBlock(t *testing.T) {
	src := t.TempDir()
	client, starts := schedulerRC(t, unfinishedJob, nil)
	s, _ := newSchedulerTestService(t, src, client)
	s.jobs["job-paused"] = &jobState{
		ID: "job-paused", Kind: "backup", State: "paused",
		AccountID: "gdrive", DestRoot: "電腦備份", Sources: []string{src},
		JobIDs: []int64{11}, StartedAt: time.Now(),
	}

	state := &schedulerState{}
	s.evaluateSchedule(state)
	evaluate := func() {
		// A second tick of the same point must not add a duplicate entry.
		state.blockedPoint = 0
		state.blockedJob = ""
		s.evaluateSchedule(state)
	}
	evaluate()

	if got := atomic.LoadInt32(starts); got != 0 {
		t.Fatalf("sync/copy calls = %d, want 0 while a paused backup holds the point", got)
	}
	point, _ := lastScheduledPoint("00:00", time.Now())
	wantID := "schedule-blocked-" + strconv.FormatInt(point.Unix(), 10)
	s.mu.Lock()
	var found []dto.Record
	for _, rec := range s.records {
		if rec.ID == wantID {
			found = append(found, rec)
		}
	}
	s.mu.Unlock()
	if len(found) != 1 {
		t.Fatalf("blocked-point records = %d, want exactly 1 (%+v)", len(found), found)
	}
	if !strings.Contains(found[0].Error, "paused") || !strings.Contains(found[0].Error, "job-paused") {
		t.Fatalf("record error = %q, want the paused job named", found[0].Error)
	}
}

// TestSchedulerFailedPointKeepsRetrying pins the retry/backoff contract.
func TestSchedulerFailedPointKeepsRetrying(t *testing.T) {
	src := t.TempDir()
	client, starts := schedulerRC(t, nil, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom","status":500}`))
	})
	s, _ := newSchedulerTestService(t, src, client)
	state := &schedulerState{}

	// The first attempts back off; the point is never given up.
	for i := 1; i <= schedulerMaxAttempts; i++ {
		state.nextAttempt = time.Now().Add(-time.Second)
		s.evaluateSchedule(state)
		if state.attempts != i {
			t.Fatalf("attempts = %d after %d failed starts, want %d", state.attempts, i, i)
		}
	}
	if state.lastPoint != 0 {
		t.Fatalf("lastPoint = %d; a failed point must stay eligible", state.lastPoint)
	}
	if !state.reported {
		t.Fatal("the long-backoff failure was not reported to history")
	}

	s.mu.Lock()
	records := append([]dto.Record(nil), s.records...)
	s.mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("history entries = %d, want exactly one failure entry", len(records))
	}
	point, _ := lastScheduledPoint("00:00", time.Now())
	if records[0].ID != scheduledFailureID(point) || records[0].State != "failed" {
		t.Fatalf("history entry = %+v, want a failed entry for %s", records[0], scheduledFailureID(point))
	}

	// Further failures keep retrying at the capped backoff without another
	// history entry or an immediate retry storm.
	for i := 0; i < 5; i++ {
		state.nextAttempt = time.Now().Add(-time.Second)
		s.evaluateSchedule(state)
	}
	if state.attempts != schedulerMaxAttempts+5 {
		t.Fatalf("attempts = %d, want %d", state.attempts, schedulerMaxAttempts+5)
	}
	if interval := time.Until(state.nextAttempt); interval > schedulerRetryMax+time.Second || interval < schedulerRetryMax-time.Minute {
		t.Fatalf("next attempt in %s, want the %s cap", interval, schedulerRetryMax)
	}
	s.mu.Lock()
	records = append([]dto.Record(nil), s.records...)
	s.mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("history entries = %d after more retries, want 1", len(records))
	}
	if got := atomic.LoadInt32(starts); got != schedulerMaxAttempts+5 {
		t.Fatalf("sync/copy calls = %d, want one per allowed attempt", got)
	}
}

func TestSchedulerBackoffGrows(t *testing.T) {
	now := time.Date(2026, 5, 10, 10, 0, 0, 0, time.Local)
	point := int64(1234)
	state := &schedulerState{}

	if state.recordFailure(point, now) {
		t.Fatal("reported a failure before the long backoff began")
	}
	if state.attemptAllowed(point, now.Add(schedulerRetryBase-time.Second)) {
		t.Fatal("attempt allowed before the first backoff elapsed")
	}
	if !state.attemptAllowed(point, now.Add(schedulerRetryBase)) {
		t.Fatal("attempt not allowed once the first backoff elapsed")
	}

	second := now.Add(schedulerRetryBase)
	if state.recordFailure(point, second) {
		t.Fatal("reported a failure before the long backoff began")
	}
	if state.attemptAllowed(point, second.Add(schedulerRetryBase)) {
		t.Fatal("second backoff did not grow")
	}
	if !state.attemptAllowed(point, second.Add(2*schedulerRetryBase)) {
		t.Fatal("attempt not allowed after the second backoff elapsed")
	}

	// Keep failing: the backoff caps and the long-backoff failure is reported
	// exactly once, not on every retry.
	reports := 0
	for i := 0; i < 10; i++ {
		if state.recordFailure(point, now) {
			reports++
		}
	}
	if state.attempts != 12 {
		t.Fatalf("attempts = %d, want 12", state.attempts)
	}
	if reports != 1 {
		t.Fatalf("reports = %d, want exactly one", reports)
	}
	if interval := state.nextAttempt.Sub(now); interval != schedulerRetryMax {
		t.Fatalf("backoff = %s, want the %s cap", interval, schedulerRetryMax)
	}

	// A new scheduled point starts with a clean slate.
	if !state.attemptAllowed(point+1, now) {
		t.Fatal("new point inherited the previous point's backoff")
	}
}

func TestScheduleDue(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 5, 10, 10, 0, 0, 0, loc)
	yesterday := now.AddDate(0, 0, -1)
	today := now
	at := func(day time.Time, hour, minute int) int64 {
		return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc).Unix()
	}
	base := func() dto.Schedule {
		return dto.Schedule{
			Enabled:     true,
			SourcePaths: []string{`C:\Users\me\Documents`},
			Time:        "09:00",
		}
	}

	tests := []struct {
		name string
		sch  dto.Schedule
		now  time.Time
		want bool
	}{
		{
			name: "disabled",
			sch:  func() dto.Schedule { s := base(); s.Enabled = false; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "empty sources",
			sch:  func() dto.Schedule { s := base(); s.SourcePaths = nil; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "empty time",
			sch:  func() dto.Schedule { s := base(); s.Time = ""; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "invalid time",
			sch:  func() dto.Schedule { s := base(); s.Time = "25:99"; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "invalid format",
			sch:  func() dto.Schedule { s := base(); s.Time = "nine"; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "after scheduled time never run",
			sch:  base(),
			now:  now,
			want: true,
		},
		{
			name: "before scheduled time never run",
			sch:  base(),
			now:  now.Add(-2 * time.Hour), // 08:00; point is yesterday 09:00
			want: true,
		},
		{
			name: "already run today",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(today, 9, 30); return s }(),
			now:  now,
			want: false,
		},
		{
			name: "ran yesterday before today's point while clock is early",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(yesterday, 9, 30); return s }(),
			now:  now.Add(-2 * time.Hour),
			want: false,
		},
		{
			name: "missed yesterday's point",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(yesterday, 8, 0); return s }(),
			now:  now.Add(-2 * time.Hour),
			want: true,
		},
		{
			name: "exactly at scheduled time, not yet run",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(yesterday, 10, 0); return s }(),
			now:  time.Date(2026, 5, 10, 9, 0, 0, 0, loc),
			want: true,
		},
		{
			name: "exactly at scheduled time, already run",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(today, 9, 0); return s }(),
			now:  time.Date(2026, 5, 10, 9, 0, 0, 0, loc),
			want: false,
		},
		{
			name: "whitespace around time is accepted",
			sch:  func() dto.Schedule { s := base(); s.Time = " 09:00 "; return s }(),
			now:  now,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scheduleDue(tc.sch, tc.now); got != tc.want {
				t.Fatalf("scheduleDue(%+v, %s) = %v, want %v", tc.sch, tc.now, got, tc.want)
			}
		})
	}
}

func TestLastScheduledPoint(t *testing.T) {
	loc := time.UTC
	tests := []struct {
		name  string
		value string
		now   time.Time
		want  time.Time
		ok    bool
	}{
		{
			name:  "today when the time has passed",
			value: "09:00",
			now:   time.Date(2026, 1, 2, 10, 15, 0, 0, loc),
			want:  time.Date(2026, 1, 2, 9, 0, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "yesterday when the time is still ahead",
			value: "09:00",
			now:   time.Date(2026, 1, 2, 8, 15, 0, 0, loc),
			want:  time.Date(2026, 1, 1, 9, 0, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "exactly at the time stays today",
			value: "09:00",
			now:   time.Date(2026, 1, 2, 9, 0, 0, 0, loc),
			want:  time.Date(2026, 1, 2, 9, 0, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "midnight",
			value: "00:00",
			now:   time.Date(2026, 1, 2, 0, 0, 0, 0, loc),
			want:  time.Date(2026, 1, 2, 0, 0, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "invalid time",
			value: "24:00",
			now:   time.Date(2026, 1, 2, 10, 0, 0, 0, loc),
			ok:    false,
		},
		{
			name:  "empty time",
			value: "",
			now:   time.Date(2026, 1, 2, 10, 0, 0, 0, loc),
			ok:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lastScheduledPoint(tc.value, tc.now)
			if ok != tc.ok {
				t.Fatalf("lastScheduledPoint(%q) ok = %v, want %v", tc.value, ok, tc.ok)
			}
			if ok && !got.Equal(tc.want) {
				t.Fatalf("lastScheduledPoint(%q) = %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}
