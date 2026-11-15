package services

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloudnest/internal/dto"
	"cloudnest/internal/rc"
)

func TestRemoteDir(t *testing.T) {
	cases := map[string]string{
		"a/b/c.txt": "a/b",
		"a.txt":     "",
		"/a/b.txt":  "a",
		"a//b.txt":  "a",
	}
	for in, want := range cases {
		if got := remoteDir(in); got != want {
			t.Errorf("remoteDir(%q) = %q, want %q", in, got, want)
		}
	}
	if base := remoteBase("a/b/c.txt"); base != "c.txt" {
		t.Errorf("remoteBase = %q", base)
	}
}

// newResumeTestService builds a Service with a fake engine serving client, so
// resume tests never need a real rclone process.
func newResumeTestService(t *testing.T, client *rc.Client) *Service {
	t.Helper()
	return &Service{
		eng:   &serviceTestEngine{client: client},
		store: newServiceStore(t),
		jobs:  make(map[string]*jobState),
	}
}

// createLargeTestFile creates a sparse file above the split threshold so
// buildSplitPlan classifies its folder source as needing a small-file sync.
func createLargeTestFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create large file: %v", err)
	}
	if err := f.Truncate(257 << 20); err != nil {
		f.Close()
		t.Fatalf("truncate large file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close large file: %v", err)
	}
	return p
}

// TestResumeJobRefusesWhileSplitRunStops pins the pause -> resume guard: Pause
// stops rclone's jobs but the split goroutine may still own the staging
// directory and job state. Resume must refuse before touching the engine or
// rclone, and leave the stored fields alone.
func TestResumeJobRefusesWhileSplitRunStops(t *testing.T) {
	j := &jobState{
		ID: "job-split", Group: "gui-job-split", Kind: "backup", State: "paused",
		Sources:  []string{filepath.Join(t.TempDir(), "video.bin")},
		DestRoot: "backup", AccountID: "acct",
		FastFiles: true, Mode: "additive", PartSizeMB: 256,
		SplitActive: true, SplitRunning: true, JobIDs: []int64{5}, TotalBytes: 4242,
	}
	s := &Service{
		// A failing engine makes it visible if the guard is bypassed and the
		// engine is asked for a client.
		eng:   &serviceTestEngine{clientErr: errors.New("engine not ready")},
		store: newServiceStore(t),
		jobs:  make(map[string]*jobState),
	}
	s.jobs[j.ID] = j

	ref := s.ResumeJob(j.ID)
	if ref.Err == nil {
		t.Fatal("ResumeJob resumed a job whose split goroutine is still running")
	}
	if !strings.Contains(ref.Err.Message, "still stopping") {
		t.Fatalf("message = %q, want a still-stopping refusal", ref.Err.Message)
	}
	s.mu.Lock()
	state, ids, total := j.State, append([]int64(nil), j.JobIDs...), j.TotalBytes
	s.mu.Unlock()
	if state != "paused" {
		t.Fatalf("state = %q after the refusal, want paused", state)
	}
	if len(ids) != 1 || ids[0] != 5 {
		t.Fatalf("job ids = %v after the refusal, want [5]", ids)
	}
	if total != 4242 {
		t.Fatalf("total bytes = %d after the refusal, want 4242", total)
	}
}

// TestResumeJobRefusesRunningJob makes sure a job which is still running is
// never started a second time.
func TestResumeJobRefusesRunningJob(t *testing.T) {
	j := &jobState{
		ID: "job-running", Group: "gui-job-running", Kind: "backup", State: "running",
		Sources: []string{filepath.Join(t.TempDir(), "docs")}, DestRoot: "backup",
		AccountID: "acct", JobIDs: []int64{7}, TotalBytes: 99,
	}
	s := &Service{
		eng:   &serviceTestEngine{clientErr: errors.New("engine not ready")},
		store: newServiceStore(t),
		jobs:  make(map[string]*jobState),
	}
	s.jobs[j.ID] = j

	ref := s.ResumeJob(j.ID)
	if ref.Err == nil {
		t.Fatal("ResumeJob resumed a job which is still running")
	}
	if !strings.Contains(ref.Err.Message, "still running") {
		t.Fatalf("message = %q, want a still-running refusal", ref.Err.Message)
	}
	s.mu.Lock()
	state, ids := j.State, append([]int64(nil), j.JobIDs...)
	s.mu.Unlock()
	if state != "running" {
		t.Fatalf("state = %q after the refusal, want running", state)
	}
	if len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("job ids = %v after the refusal, want [7]", ids)
	}
}

// TestResumeJobRefusesFinishedJob keeps a stale History row (a by-id retry
// keeps the id) from starting a second run of a backup which already finished.
func TestResumeJobRefusesFinishedJob(t *testing.T) {
	j := &jobState{
		ID: "job-done", Group: "gui-job-done", Kind: "backup", State: "done",
		Sources: []string{filepath.Join(t.TempDir(), "docs")}, DestRoot: "backup",
		AccountID: "acct", JobIDs: []int64{9}, TotalBytes: 10,
	}
	s := &Service{
		eng:   &serviceTestEngine{clientErr: errors.New("engine not ready")},
		store: newServiceStore(t),
		jobs:  make(map[string]*jobState),
	}
	s.jobs[j.ID] = j

	ref := s.ResumeJob(j.ID)
	if ref.Err == nil {
		t.Fatal("ResumeJob restarted a job which already finished")
	}
	if !strings.Contains(ref.Err.Message, "already finished") {
		t.Fatalf("message = %q, want an already-finished refusal", ref.Err.Message)
	}
	s.mu.Lock()
	state := j.State
	s.mu.Unlock()
	if state != "done" {
		t.Fatalf("state = %q after the refusal, want done", state)
	}
}

// TestResumeJobResumesPausedNonSplitJob keeps the ordinary resume semantics
// unchanged: a paused backup without a split goroutine starts its sync again.
func TestResumeJobResumesPausedNonSplitJob(t *testing.T) {
	src := t.TempDir()
	var syncCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/copy" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&syncCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 42})
	}))
	t.Cleanup(srv.Close)

	j := &jobState{
		ID: "job-sync", Group: "gui-job-sync", Kind: "backup", State: "paused",
		Sources: []string{src}, DestRoot: "backup", AccountID: "acct",
		JobIDs: []int64{5}, TotalBytes: 99,
	}
	s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
	s.jobs[j.ID] = j

	ref := s.ResumeJob(j.ID)
	if ref.Err != nil {
		t.Fatalf("ResumeJob: %+v", ref.Err)
	}
	if got := atomic.LoadInt32(&syncCalls); got != 1 {
		t.Fatalf("sync/copy calls = %d, want 1", got)
	}
	if ref.JobID != 42 {
		t.Fatalf("job id = %d, want 42", ref.JobID)
	}
	s.mu.Lock()
	state, ids, resuming := j.State, append([]int64(nil), j.JobIDs...), j.Resuming
	s.mu.Unlock()
	if state != "running" {
		t.Fatalf("state = %q after resume, want running", state)
	}
	if len(ids) != 1 || ids[0] != 42 {
		t.Fatalf("job ids = %v after resume, want [42]", ids)
	}
	if resuming {
		t.Fatal("Resuming is still set after a successful resume")
	}
}

// TestResumeJobKeepsExcludesAndMode pins that a resumed non-split backup
// uploads exactly what the original plan did: the stored exclude patterns are
// passed to rclone (so a file excluded from the first run is not uploaded by
// the resume) and a mirror backup keeps its sync/sync method.
func TestResumeJobKeepsExcludesAndMode(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		method string
	}{
		{name: "additive", mode: "additive", method: "/sync/copy"},
		{name: "mirror", mode: "mirror", method: "/sync/sync"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := t.TempDir()
			// A file the original backup was configured to exclude.
			if err := os.WriteFile(filepath.Join(src, "secret.tmp"), []byte("x"), 0o644); err != nil {
				t.Fatalf("write excluded file: %v", err)
			}

			filters := make(chan map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.method {
					http.NotFound(w, r)
					return
				}
				var body struct {
					Filter map[string]any `json:"_filter"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				filters <- body.Filter
				_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 42})
			}))
			t.Cleanup(srv.Close)

			j := &jobState{
				ID: "job-excl", Group: "gui-job-excl", Kind: "backup", State: "paused",
				Sources: []string{src}, DestRoot: "backup", AccountID: "acct",
				Mode: tc.mode, Excludes: []string{"*.tmp"}, JobIDs: []int64{5},
			}
			s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
			s.jobs[j.ID] = j

			ref := s.ResumeJob(j.ID)
			if ref.Err != nil {
				t.Fatalf("ResumeJob: %+v", ref.Err)
			}
			var filter map[string]any
			select {
			case filter = <-filters:
			case <-time.After(5 * time.Second):
				t.Fatalf("the resumed job never reached %s", tc.method)
			}
			if filter == nil {
				t.Fatal("resumed sync carried no _filter; the excluded file would be uploaded")
			}
			got, _ := filter["ExcludeRule"].([]any)
			if len(got) != 2 || got[0] != "*.tmp" || got[1] != "*.tmp/**" {
				t.Fatalf("_filter.ExcludeRule = %v, want [*.tmp *.tmp/**]", filter["ExcludeRule"])
			}
		})
	}
}

// TestResumeJobClearsResumingOnFailure makes sure a resume which cannot start
// its jobs restores the pre-resume state and clears the in-flight marker, so
// the job does not look running and a later retry is accepted.
func TestResumeJobClearsResumingOnFailure(t *testing.T) {
	src := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/copy":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "boom", "status": 500})
		case "/job/stopgroup":
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	j := &jobState{
		ID: "job-fail", Group: "gui-job-fail", Kind: "backup", State: "paused",
		Sources: []string{src}, DestRoot: "backup", AccountID: "acct",
		JobIDs: []int64{5}, TotalBytes: 99,
	}
	s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
	s.jobs[j.ID] = j

	ref := s.ResumeJob(j.ID)
	if ref.Err == nil {
		t.Fatal("ResumeJob succeeded although sync/copy failed")
	}
	s.mu.Lock()
	state, resuming, ids := j.State, j.Resuming, append([]int64(nil), j.JobIDs...)
	s.mu.Unlock()
	if state != "paused" {
		t.Fatalf("state = %q after the failed resume, want paused", state)
	}
	if resuming {
		t.Fatal("Resuming is still set after a failed resume")
	}
	if len(ids) != 1 || ids[0] != 5 {
		t.Fatalf("job ids = %v after the failed resume, want the original [5]", ids)
	}
}

// TestResumeJobAbortsWhenStopStartsDuringResume reproduces the interleaving
// where PauseJob's job/stopgroup runs before the resume's new rclone job
// exists: the stop sequence bump must make the resume stop what it started and
// refuse, instead of committing a job the pause can no longer see.
func TestResumeJobAbortsWhenStopStartsDuringResume(t *testing.T) {
	src := t.TempDir()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var stopGroups int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/copy":
			started <- struct{}{}
			<-release
			_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 42})
		case "/job/stopgroup":
			atomic.AddInt32(&stopGroups, 1)
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	j := &jobState{
		ID: "job-race", Group: "gui-job-race", Kind: "backup", State: "paused",
		Sources: []string{src}, DestRoot: "backup", AccountID: "acct",
		JobIDs: []int64{5},
	}
	s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
	s.jobs[j.ID] = j

	done := make(chan dto.JobRef, 1)
	go func() { done <- s.ResumeJob(j.ID) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the resume never started its sync/copy")
	}

	// The user pauses while the resume is still waiting for rclone to answer.
	s.PauseJob(j.ID)
	close(release)

	ref := <-done
	if ref.Err == nil {
		t.Fatal("resume committed although a pause started while it was starting jobs")
	}
	if !strings.Contains(ref.Err.Message, "paused or cancelled") {
		t.Fatalf("message = %q, want an interrupted-resume refusal", ref.Err.Message)
	}
	s.mu.Lock()
	state, ids, resuming := j.State, append([]int64(nil), j.JobIDs...), j.Resuming
	s.mu.Unlock()
	if state != "paused" {
		t.Fatalf("state = %q after the race, want paused", state)
	}
	if len(ids) != 1 || ids[0] != 5 {
		t.Fatalf("job ids = %v after the race, want the old [5]", ids)
	}
	if resuming {
		t.Fatal("Resuming is still set after the aborted resume")
	}
	if got := atomic.LoadInt32(&stopGroups); got < 2 {
		t.Fatalf("job/stopgroup calls = %d, want at least 2 (the pause and the abort)", got)
	}
}

// TestResumeJobRefusesWhileResuming pins the second guard: a Pause landing
// during a resume restores "paused" while the attempt is still in flight, so
// the state check alone would let a second click start a duplicate. The
// in-flight marker must refuse it on its own.
func TestResumeJobRefusesWhileResuming(t *testing.T) {
	j := &jobState{
		ID: "job-resuming", Group: "gui-job-resuming", Kind: "backup", State: "paused",
		Sources:  []string{filepath.Join(t.TempDir(), "docs")},
		DestRoot: "backup", AccountID: "acct", JobIDs: []int64{5}, Resuming: true,
	}
	s := &Service{
		// A failing engine makes it visible if the guard is bypassed.
		eng:   &serviceTestEngine{clientErr: errors.New("engine not ready")},
		store: newServiceStore(t),
		jobs:  make(map[string]*jobState),
	}
	s.jobs[j.ID] = j

	ref := s.ResumeJob(j.ID)
	if ref.Err == nil {
		t.Fatal("ResumeJob was accepted while another resume was in flight")
	}
	if !strings.Contains(ref.Err.Message, "still resuming") {
		t.Fatalf("message = %q, want a still-resuming refusal", ref.Err.Message)
	}
	s.mu.Lock()
	state, resuming := j.State, j.Resuming
	s.mu.Unlock()
	if state != "paused" || !resuming {
		t.Fatalf("state = %q resuming = %v after the refusal, want paused/true", state, resuming)
	}
}

// TestActiveJobsLeavesResumingJobAlone reproduces the Activity poll which used
// to terminalise a job mid-resume. ResumeJob has reserved the job (State
// "running", Resuming set) but the old, already stopped ids are still
// installed and rclone reports them as expired. ActiveJobs must leave the job
// to the resume instead of writing a History record which makes the resume
// refuse its own result.
func TestActiveJobsLeavesResumingJobAlone(t *testing.T) {
	src := t.TempDir()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/copy":
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 42})
		case "/core/stats":
			_, _ = w.Write([]byte("{}"))
		case "/job/status":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"job not found","status":500}`))
		case "/job/stopgroup":
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	j := &jobState{
		ID: "job-resume", Group: "gui-job-resume", Kind: "backup", State: "paused",
		Sources: []string{src}, DestRoot: "backup", AccountID: "acct",
		JobIDs: []int64{5}, TotalBytes: 99,
	}
	s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
	s.jobs[j.ID] = j

	resumed := make(chan dto.JobRef, 1)
	go func() { resumed <- s.ResumeJob(j.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("resume did not reach sync/copy")
	}

	// The old id 5 is unknown to the same engine, so without the in-flight
	// marker ActiveJobs would write "done" here.
	s.ActiveJobs()

	s.mu.Lock()
	state, resuming, records := j.State, j.Resuming, len(s.records)
	s.mu.Unlock()
	if !resuming {
		t.Fatal("Resuming was cleared while the resume was still in flight")
	}
	if state != "running" {
		t.Fatalf("state = %q after the concurrent poll, want running (the resume was terminalised)", state)
	}
	if records != 0 {
		t.Fatalf("history records = %d after the concurrent poll, want 0", records)
	}

	close(release)
	ref := <-resumed
	if ref.Err != nil {
		t.Fatalf("ResumeJob: %+v", ref.Err)
	}
	s.mu.Lock()
	state, resuming, ids := j.State, j.Resuming, append([]int64(nil), j.JobIDs...)
	s.mu.Unlock()
	if state != "running" || resuming {
		t.Fatalf("after the resume state = %q resuming = %v, want running/false", state, resuming)
	}
	if len(ids) != 1 || ids[0] != 42 {
		t.Fatalf("job ids = %v, want [42]", ids)
	}
}

// TestResumeJobRefusesSecondSplitResume proves a double click cannot launch a
// second split goroutine over the same staging directory: the first call flips
// the job to running and sets the liveness flag before returning, so the next
// call is refused.
func TestResumeJobRefusesSecondSplitResume(t *testing.T) {
	src := filepath.Join(t.TempDir(), "video.bin")
	f, err := os.Create(src)
	if err != nil {
		t.Fatalf("create large file: %v", err)
	}
	if err := f.Truncate(257 << 20); err != nil {
		f.Close()
		t.Fatalf("truncate large file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close large file: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "boom", "status": 500})
	}))
	t.Cleanup(srv.Close)

	j := &jobState{
		ID: "job-split", Group: "gui-job-split", Kind: "backup", State: "paused",
		Sources: []string{src}, DestRoot: "backup", AccountID: "acct",
		FastFiles: true, Mode: "additive", PartSizeMB: 256,
		SplitActive: true, JobIDs: []int64{5},
	}
	s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
	s.jobs[j.ID] = j

	first := s.ResumeJob(j.ID)
	if first.Err != nil {
		t.Fatalf("first ResumeJob: %+v", first.Err)
	}
	s.mu.Lock()
	state, splitRunning, resuming := j.State, j.SplitRunning, j.Resuming
	s.mu.Unlock()
	if state != "running" || !splitRunning {
		t.Fatalf("after the first resume state = %q, split running = %v, want running/true",
			state, splitRunning)
	}
	if resuming {
		t.Fatal("Resuming is still set after a successful split resume")
	}

	second := s.ResumeJob(j.ID)
	if second.Err == nil {
		t.Fatal("the second ResumeJob was accepted")
	}
	if !strings.Contains(second.Err.Message, "still running") {
		t.Fatalf("second message = %q, want a still-running refusal", second.Err.Message)
	}

	// Wait for the split goroutine to reach a terminal state so it does not
	// touch the test state or the source file after the test returns.
	deadline := time.Now().Add(60 * time.Second)
	for {
		s.mu.Lock()
		state := j.State
		s.mu.Unlock()
		switch state {
		case "failed", "cancelled", "done":
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("split goroutine did not finish, state = %q", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestResumeSplitJobDoesNotOverwritePauseOrCancel reproduces a Pause/Cancel
// landing while a split resume is between validation and ownership: the resume
// must not flip the job back to running or launch the pipeline, and it must
// stop the rclone job it started in that window.
func TestResumeSplitJobDoesNotOverwritePauseOrCancel(t *testing.T) {
	tests := []struct {
		name      string
		wantState string
		act       func(s *Service, id string)
	}{
		{name: "pause", wantState: "paused", act: func(s *Service, id string) { s.PauseJob(id) }},
		{name: "cancel", wantState: "cancelled", act: func(s *Service, id string) { s.CancelJob(id) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srcDir := t.TempDir()
			createLargeTestFile(t, srcDir, "video.bin")

			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var stopGroups int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/sync/copy":
					// The filtered small-file sync: block until the concurrent
					// Pause/Cancel has written its state.
					select {
					case entered <- struct{}{}:
					default:
					}
					<-release
					_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 77})
				case "/job/stopgroup":
					atomic.AddInt32(&stopGroups, 1)
					_, _ = w.Write([]byte("{}"))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)

			j := &jobState{
				ID: "job-split", Group: "gui-job-split", Kind: "backup", State: "paused",
				Sources: []string{srcDir}, DestRoot: "backup", AccountID: "acct",
				FastFiles: true, Mode: "additive", PartSizeMB: 256,
				SplitActive: true, JobIDs: []int64{5},
			}
			s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
			s.jobs[j.ID] = j

			resumed := make(chan dto.JobRef, 1)
			go func() { resumed <- s.ResumeJob(j.ID) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("resume did not reach the small-file sync")
			}
			tc.act(s, j.ID)
			close(release)

			ref := <-resumed
			if ref.Err == nil {
				t.Fatalf("ResumeJob succeeded although the job was %s during the resume", tc.name)
			}

			s.mu.Lock()
			state, splitRunning, resuming := j.State, j.SplitRunning, j.Resuming
			ids := append([]int64(nil), j.JobIDs...)
			s.mu.Unlock()
			if state != tc.wantState {
				t.Fatalf("state = %q after the refused resume, want %q (the %s was overwritten)",
					state, tc.wantState, tc.name)
			}
			if splitRunning {
				t.Fatal("SplitRunning is true after a refused split resume")
			}
			if resuming {
				t.Fatal("Resuming is still set after a refused split resume")
			}
			if len(ids) != 1 || ids[0] != 5 {
				t.Fatalf("job ids = %v after the refusal, want the original [5]", ids)
			}
			// One stopgroup from Pause/Cancel, one from the refusal aborting the
			// jobs this attempt started.
			if got := atomic.LoadInt32(&stopGroups); got < 2 {
				t.Fatalf("job/stopgroup calls = %d, want at least 2 (the started job must be stopped)", got)
			}
		})
	}
}

// TestResumeJobRefusesOverlappingResume proves a second ResumeJob cannot start a
// duplicate set of jobs while the first is still starting: ownership is
// reserved atomically before the RC calls, and the loser is refused.
func TestResumeJobRefusesOverlappingResume(t *testing.T) {
	src := t.TempDir()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var syncCalls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/copy" {
			http.NotFound(w, r)
			return
		}
		n := atomic.AddInt32(&syncCalls, 1)
		if n == 1 {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jobid": int64(40 + n)})
	}))
	t.Cleanup(srv.Close)

	j := &jobState{
		ID: "job-sync", Group: "gui-job-sync", Kind: "backup", State: "paused",
		Sources: []string{src}, DestRoot: "backup", AccountID: "acct",
		JobIDs: []int64{5},
	}
	s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
	s.jobs[j.ID] = j

	first := make(chan dto.JobRef, 1)
	go func() { first <- s.ResumeJob(j.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first resume did not reach sync/copy")
	}

	second := s.ResumeJob(j.ID)
	if second.Err == nil {
		t.Fatal("the overlapping ResumeJob was accepted and started a duplicate")
	}
	if !strings.Contains(second.Err.Message, "still running") {
		t.Fatalf("second message = %q, want a still-running refusal", second.Err.Message)
	}
	if got := atomic.LoadInt32(&syncCalls); got != 1 {
		t.Fatalf("sync/copy calls = %d before releasing the first resume, want 1", got)
	}

	close(release)
	ref := <-first
	if ref.Err != nil {
		t.Fatalf("first ResumeJob: %+v", ref.Err)
	}
	if got := atomic.LoadInt32(&syncCalls); got != 1 {
		t.Fatalf("sync/copy calls = %d after both resumes, want 1", got)
	}
	s.mu.Lock()
	state, ids := j.State, append([]int64(nil), j.JobIDs...)
	s.mu.Unlock()
	if state != "running" {
		t.Fatalf("state = %q after the successful resume, want running", state)
	}
	if len(ids) != 1 || ids[0] != 41 {
		t.Fatalf("job ids = %v, want [41] from the winning resume", ids)
	}
}

// TestUploadStopsStartedJobsWhenALaterSourceFails pins the abort contract for
// Upload: a job which cannot be registered must not leave earlier rclone jobs
// transferring invisibly.
func TestUploadStopsStartedJobsWhenALaterSourceFails(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()

	started := make(chan string, 1)
	stopped := make(chan string, 1)
	var syncCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/copy":
			var in struct {
				Group string `json:"_group"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if atomic.AddInt32(&syncCalls, 1) == 1 {
				started <- in.Group
				_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 11})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "boom", "status": 500})
		case "/job/stopgroup":
			var in struct {
				Group string `json:"group"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			stopped <- in.Group
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
	ref := s.Upload("acct", "dest", []string{first, second})

	if ref.Err == nil {
		t.Fatal("Upload succeeded although the second job could not start")
	}
	if got := atomic.LoadInt32(&syncCalls); got != 2 {
		t.Fatalf("sync/copy calls = %d, want 2", got)
	}
	wantGroup := <-started
	select {
	case gotGroup := <-stopped:
		if gotGroup != wantGroup {
			t.Fatalf("stopped group = %q, want %q", gotGroup, wantGroup)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the already-started upload job was not stopped")
	}
	s.mu.Lock()
	jobs := len(s.jobs)
	s.mu.Unlock()
	if jobs != 0 {
		t.Fatalf("job map has %d entries after a failed upload, want 0", jobs)
	}
}

// TestDownloadStopsStartedJobsWhenALaterSourceFails is the Download half of the
// same contract.
func TestDownloadStopsStartedJobsWhenALaterSourceFails(t *testing.T) {
	localDir := t.TempDir()

	started := make(chan string, 1)
	stopped := make(chan string, 1)
	var syncCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/operations/stat":
			_ = json.NewEncoder(w).Encode(map[string]any{"item": map[string]any{
				"Path": "", "Name": "dir", "IsDir": true,
			}})
		case "/sync/copy":
			var in struct {
				Group string `json:"_group"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if atomic.AddInt32(&syncCalls, 1) == 1 {
				started <- in.Group
				_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 21})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "boom", "status": 500})
		case "/job/stopgroup":
			var in struct {
				Group string `json:"group"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			stopped <- in.Group
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	s := newResumeTestService(t, rc.New(srv.URL, "", "", nil))
	ref := s.Download("acct", []string{"one", "two"}, localDir)

	if ref.Err == nil {
		t.Fatal("Download succeeded although the second job could not start")
	}
	if got := atomic.LoadInt32(&syncCalls); got != 2 {
		t.Fatalf("sync/copy calls = %d, want 2", got)
	}
	wantGroup := <-started
	select {
	case gotGroup := <-stopped:
		if gotGroup != wantGroup {
			t.Fatalf("stopped group = %q, want %q", gotGroup, wantGroup)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the already-started download job was not stopped")
	}
	s.mu.Lock()
	jobs := len(s.jobs)
	s.mu.Unlock()
	if jobs != 0 {
		t.Fatalf("job map has %d entries after a failed download, want 0", jobs)
	}
}
