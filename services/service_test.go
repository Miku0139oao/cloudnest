package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloudnest/internal/dto"
	"cloudnest/internal/engine"
	"cloudnest/internal/rc"
	"cloudnest/internal/settings"
)

// serviceTestEngine serves a fixed rc.Client and counts engine restarts so the
// restart path can be tested without an rclone process.
type serviceTestEngine struct {
	engine.Engine
	client    *rc.Client
	clientErr error
	status    engine.Status
	restarts  int
	// restartCtxErr records the state of the context handed to Restart, so a
	// test can prove a slow unmount did not consume the restart budget.
	restartCtxErr error
	onRestart     func()
}

func (e *serviceTestEngine) Client() (*rc.Client, error) {
	if e.clientErr != nil {
		return nil, e.clientErr
	}
	return e.client, nil
}

func (e *serviceTestEngine) Status() engine.Status { return e.status }

func (e *serviceTestEngine) Restart(ctx context.Context) error {
	e.restarts++
	e.restartCtxErr = ctx.Err()
	if e.onRestart != nil {
		e.onRestart()
	}
	return nil
}

func (e *serviceTestEngine) ApplyRuntime(bool, bool) {}

func newServiceStore(t *testing.T) *settings.Store {
	t.Helper()
	store, err := settings.New(t.TempDir())
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}
	return store
}

// ---------------------------------------------------------------- schedule

func TestSaveSettingsRemembersLastRunWhenScheduleUnchanged(t *testing.T) {
	lastRun := time.Now().Add(-time.Hour).Unix()
	sch := dto.Schedule{
		Enabled: true, Time: "03:00", AccountID: "gdrive", DestRoot: "backup",
		Mode: "additive", SourcePaths: []string{`C:\docs`}, LastRun: lastRun,
	}
	store := newServiceStore(t)
	if err := store.Update(func(cur *dto.Settings) { cur.Schedule = sch }); err != nil {
		t.Fatalf("store.Update: %v", err)
	}
	s := &Service{store: store}

	// An unrelated settings save must not make the schedule due.
	s.SaveSettings(dto.Settings{Schedule: sch, Language: "en"})

	if got := store.Get().Schedule.LastRun; got != lastRun {
		t.Fatalf("LastRun = %d, want the preserved %d", got, lastRun)
	}
}

func TestSaveSettingsEnablingScheduleDoesNotCatchUp(t *testing.T) {
	lastRun := time.Now().Add(-25 * time.Hour).Unix()
	sch := dto.Schedule{
		Enabled: false, Time: "03:00", AccountID: "gdrive", DestRoot: "backup",
		Mode: "additive", SourcePaths: []string{`C:\docs`}, LastRun: lastRun,
	}
	store := newServiceStore(t)
	if err := store.Update(func(cur *dto.Settings) { cur.Schedule = sch }); err != nil {
		t.Fatalf("store.Update: %v", err)
	}
	s := &Service{store: store}

	enabled := sch
	enabled.Enabled = true
	started := time.Now().Unix()
	s.SaveSettings(dto.Settings{Schedule: enabled})

	got := store.Get().Schedule
	if got.LastRun < started {
		t.Fatalf("LastRun = %d, want a fresh timestamp >= %d after enabling", got.LastRun, started)
	}
	if scheduleDue(got, time.Now()) {
		t.Fatalf("enabling the schedule made it due immediately (LastRun %d)", got.LastRun)
	}
}

func TestSaveSettingsResetsLastRunWhenModeChanges(t *testing.T) {
	lastRun := time.Now().Add(-25 * time.Hour).Unix()
	sch := dto.Schedule{
		Enabled: true, Time: "03:00", AccountID: "gdrive", DestRoot: "backup",
		Mode: "additive", SourcePaths: []string{`C:\docs`}, LastRun: lastRun,
	}
	store := newServiceStore(t)
	if err := store.Update(func(cur *dto.Settings) { cur.Schedule = sch }); err != nil {
		t.Fatalf("store.Update: %v", err)
	}
	s := &Service{store: store}

	changed := sch
	changed.Mode = "mirror"
	started := time.Now().Unix()
	s.SaveSettings(dto.Settings{Schedule: changed})

	if got := store.Get().Schedule.LastRun; got < started {
		t.Fatalf("LastRun = %d, want a fresh timestamp >= %d after a mode change", got, started)
	}
}

// ---------------------------------------------------------------- mounts

func TestMountSnapshotFrom(t *testing.T) {
	tests := []struct {
		in   dto.MountInfo
		want mountSnapshot
		ok   bool
	}{
		{
			in:   dto.MountInfo{Fs: "gdrive:Photos/2026", MountPoint: "X:"},
			want: mountSnapshot{accountID: "gdrive", remotePath: "Photos/2026", mountPoint: "X:"},
			ok:   true,
		},
		{
			in:   dto.MountInfo{Fs: "onedrive:", MountPoint: "Y:"},
			want: mountSnapshot{accountID: "onedrive", remotePath: "", mountPoint: "Y:"},
			ok:   true,
		},
		{in: dto.MountInfo{Fs: "local-folder", MountPoint: "Z:"}, ok: false},
		{in: dto.MountInfo{Fs: ":Photos", MountPoint: "Z:"}, ok: false},
		{in: dto.MountInfo{Fs: "gdrive:", MountPoint: ""}, ok: false},
	}
	for _, tc := range tests {
		got, ok := mountSnapshotFrom(tc.in)
		if ok != tc.ok {
			t.Fatalf("mountSnapshotFrom(%+v) ok = %v, want %v", tc.in, ok, tc.ok)
		}
		if ok && got != tc.want {
			t.Fatalf("mountSnapshotFrom(%+v) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestRestartEngineDetachesMountsBeforeRestart(t *testing.T) {
	var unmounts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mount/listmounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"mountPoints": []map[string]any{
				{"Fs": "gdrive:Photos", "MountPoint": "X:"},
				{"Fs": "onedrive:", "MountPoint": "Y:"},
			}})
		case "/mount/unmount":
			atomic.AddInt32(&unmounts, 1)
			_, _ = w.Write([]byte("{}"))
		case "/mount/mount":
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	eng := &serviceTestEngine{client: rc.New(server.URL, "", "", nil)}
	eng.onRestart = func() {
		if got := atomic.LoadInt32(&unmounts); got != 2 {
			t.Errorf("engine restarted after %d unmounts, want 2 (mounts would dangle)", got)
		}
	}
	s := &Service{eng: eng}

	s.RestartEngine()

	if eng.restarts != 1 {
		t.Fatalf("engine restarts = %d, want 1", eng.restarts)
	}
}

// TestRestartEngineAbortsWhenMountsCannotBeListed pins the contract that a
// listing failure while the engine is ready aborts the restart: proceeding
// would stop the daemon hosting the mount and strand the drive letter.
func TestRestartEngineAbortsWhenMountsCannotBeListed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mount/listmounts" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom","status":500}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady},
	}
	s := &Service{eng: eng}

	if err := s.restartEngineWithMounts(context.Background()); err == nil {
		t.Fatal("restart proceeded although the engine could not list its mounts")
	}
	if eng.restarts != 0 {
		t.Fatalf("engine restarts = %d, want 0 (the drive could have been stranded)", eng.restarts)
	}
}

// TestRestartEngineProceedsWhenEngineIsNotRunning covers the other half of the
// contract: with no engine there is no mount to strand, so the restart runs.
func TestRestartEngineProceedsWhenEngineIsNotRunning(t *testing.T) {
	eng := &serviceTestEngine{
		clientErr: engine.ErrNotReady,
		status:    engine.Status{State: engine.StateStopped},
	}
	s := &Service{eng: eng}

	if err := s.restartEngineWithMounts(context.Background()); err != nil {
		t.Fatalf("restartEngineWithMounts: %v", err)
	}
	if eng.restarts != 1 {
		t.Fatalf("engine restarts = %d, want 1", eng.restarts)
	}
}

// TestRestartEngineSerialisesConcurrentRestarts proves two overlapping restart
// requests do not interleave detach/restart/reattach. Without the Service-level
// lock the second Restart would run while the first is still inside it.
func TestRestartEngineSerialisesConcurrentRestarts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mount/listmounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"mountPoints": []map[string]any{
				{"Fs": "gdrive:Photos", "MountPoint": "X:"},
			}})
		case "/mount/unmount", "/mount/mount":
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var active, maxActive int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady},
	}
	eng.onRestart = func() {
		n := atomic.AddInt32(&active, 1)
		defer atomic.AddInt32(&active, -1)
		for {
			cur := atomic.LoadInt32(&maxActive)
			if n <= cur || atomic.CompareAndSwapInt32(&maxActive, cur, n) {
				break
			}
		}
		enteredOnce.Do(func() { close(entered) })
		<-release
	}
	s := &Service{eng: eng}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = s.restartEngineWithMounts(context.Background())
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first restart did not reach the engine")
	}
	go func() {
		defer wg.Done()
		_ = s.restartEngineWithMounts(context.Background())
	}()
	// Give the second call time to (incorrectly) enter the sequence.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("concurrent restarts = %d, want 1 (overlapping sequences lose mounts)", got)
	}
	if eng.restarts != 2 {
		t.Fatalf("engine restarts = %d, want 2", eng.restarts)
	}
}

// TestActiveJobsDoesNotOverwritePauseOrCancel reproduces the race where a
// Pause/Cancel lands while ActiveJobs is waiting on job/status: the terminal
// transition and the History record must not clobber the user's action.
func TestActiveJobsDoesNotOverwritePauseOrCancel(t *testing.T) {
	tests := []struct {
		name        string
		act         func(s *Service, id string)
		wantState   string
		wantRecords int
		wantRecord  string
	}{
		{
			name:        "pause",
			act:         func(s *Service, id string) { s.PauseJob(id) },
			wantState:   "paused",
			wantRecords: 0,
		},
		{
			name:        "cancel",
			act:         func(s *Service, id string) { s.CancelJob(id) },
			wantState:   "cancelled",
			wantRecords: 1,
			wantRecord:  "cancelled",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/core/stats":
					_, _ = w.Write([]byte("{}"))
				case "/job/status":
					select {
					case entered <- struct{}{}:
					default:
					}
					<-release
					_, _ = w.Write([]byte(`{"id":1,"finished":true,"success":true}`))
				case "/job/stopgroup":
					_, _ = w.Write([]byte("{}"))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			s := &Service{
				eng:  &serviceTestEngine{client: rc.New(server.URL, "", "", nil)},
				jobs: make(map[string]*jobState),
			}
			s.jobs["job-1"] = &jobState{
				ID: "job-1", Group: "gui-job-1", JobIDs: []int64{1},
				Kind: "backup", State: "running", Title: "docs", StartedAt: time.Now(),
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				s.ActiveJobs()
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("ActiveJobs did not reach job/status")
			}
			tc.act(s, "job-1")
			close(release)
			<-done

			s.mu.Lock()
			state := s.jobs["job-1"].State
			records := append([]dto.Record(nil), s.records...)
			s.mu.Unlock()
			if state != tc.wantState {
				t.Fatalf("job state = %q, want %q (ActiveJobs overwrote the user action)", state, tc.wantState)
			}
			if len(records) != tc.wantRecords {
				t.Fatalf("history records = %d, want %d", len(records), tc.wantRecords)
			}
			if tc.wantRecords == 1 && records[0].State != tc.wantRecord {
				t.Fatalf("history state = %q, want %q", records[0].State, tc.wantRecord)
			}
		})
	}
}

// ---------------------------------------------------------------- StartBackup

func TestStartBackupValidatesSourcesBeforeStartingJobs(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.NotFound(w, r)
	}))
	defer server.Close()

	s := &Service{
		eng:   &serviceTestEngine{client: rc.New(server.URL, "", "", nil)},
		store: newServiceStore(t),
		jobs:  make(map[string]*jobState),
	}

	ref := s.StartBackup(dto.PlanRequest{
		SourcePaths: []string{dir, missing}, AccountID: "acct", DestRoot: "backup",
	})

	if ref.Err == nil {
		t.Fatal("StartBackup succeeded with a missing source")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("rclone was called %d times before the sources were validated, want 0", got)
	}
	if len(s.jobs) != 0 {
		t.Fatalf("job map has %d entries after a failed start", len(s.jobs))
	}
}

func TestStartBackupStopsJobsWhenASourceFailsToStart(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()

	var mu sync.Mutex
	var group, stoppedGroup string
	var syncCalls, stopCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/copy":
			var in struct {
				Group string `json:"_group"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if n := atomic.AddInt32(&syncCalls, 1); n == 1 {
				mu.Lock()
				group = in.Group
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 11})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "boom", "status": 500})
		case "/job/stopgroup":
			atomic.AddInt32(&stopCalls, 1)
			var in struct {
				Group string `json:"group"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			mu.Lock()
			stoppedGroup = in.Group
			mu.Unlock()
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{
		eng:   &serviceTestEngine{client: rc.New(server.URL, "", "", nil)},
		store: newServiceStore(t),
		jobs:  make(map[string]*jobState),
	}

	ref := s.StartBackup(dto.PlanRequest{
		SourcePaths: []string{first, second}, AccountID: "acct", DestRoot: "backup",
	})

	if ref.Err == nil {
		t.Fatal("StartBackup succeeded although the second job could not start")
	}
	if got := atomic.LoadInt32(&syncCalls); got != 2 {
		t.Fatalf("sync/copy calls = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&stopCalls); got != 1 {
		t.Fatalf("job/stopgroup calls = %d, want 1 (the already-started job must be stopped)", got)
	}
	mu.Lock()
	wantGroup, gotGroup := group, stoppedGroup
	mu.Unlock()
	if gotGroup != wantGroup || gotGroup == "" {
		t.Fatalf("stopped group = %q, want %q", gotGroup, wantGroup)
	}
	if len(s.jobs) != 0 {
		t.Fatalf("job map has %d entries after a failed start", len(s.jobs))
	}
	if len(s.records) != 0 {
		t.Fatalf("history has %d entries after a failed start", len(s.records))
	}
}

// ---------------------------------------------------------------- resume/split

// TestStartBackupRecordsSplitIntent pins the split metadata a later ResumeJob
// depends on: the fast-files switch, mode, excludes and the part size actually
// used (not whatever Settings says at resume time).
func TestStartBackupRecordsSplitIntent(t *testing.T) {
	src := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/copy" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 7})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := newServiceStore(t)
	if err := store.Update(func(cur *dto.Settings) { cur.Backup.PartSizeMB = 512 }); err != nil {
		t.Fatalf("store.Update: %v", err)
	}
	s := &Service{
		eng:   &serviceTestEngine{client: rc.New(server.URL, "", "", nil)},
		store: store,
		jobs:  make(map[string]*jobState),
	}

	ref := s.StartBackup(dto.PlanRequest{
		SourcePaths: []string{src}, AccountID: "acct", DestRoot: "backup",
		Mode: "additive", FastFiles: true, Excludes: []string{"*.tmp"},
	})
	if ref.Err != nil {
		t.Fatalf("StartBackup: %+v", ref.Err)
	}

	s.mu.Lock()
	j := s.jobs[ref.ID]
	s.mu.Unlock()
	if j == nil {
		t.Fatalf("job %q was not registered", ref.ID)
	}
	if !j.FastFiles || j.Mode != "additive" || j.PartSizeMB != 512 {
		t.Fatalf("split intent = fast=%v mode=%q partSize=%d, want true/additive/512",
			j.FastFiles, j.Mode, j.PartSizeMB)
	}
	if len(j.Excludes) != 1 || j.Excludes[0] != "*.tmp" {
		t.Fatalf("excludes = %v, want [*.tmp]", j.Excludes)
	}
	if j.SplitActive {
		t.Fatal("SplitActive is true although the source has no large file")
	}
}

// TestResumeJobRefusesWhenSplitPlanCannotBeRebuilt checks that a split job is
// never downgraded to a whole-file sync when its plan is gone: both a missing
// source and an empty plan must return the explicit refusal.
func TestResumeJobRefusesWhenSplitPlanCannotBeRebuilt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	empty := t.TempDir()

	tests := []struct {
		name string
		src  string
	}{
		{name: "missing source", src: missing},
		{name: "no large files left", src: empty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				http.NotFound(w, r)
			}))
			defer server.Close()

			s := &Service{
				eng:   &serviceTestEngine{client: rc.New(server.URL, "", "", nil)},
				store: newServiceStore(t),
				jobs:  make(map[string]*jobState),
			}
			j := &jobState{
				ID: "job-split", Group: "gui-job-split", Kind: "backup", State: "paused",
				Sources: []string{tc.src}, DestRoot: "backup", AccountID: "acct",
				FastFiles: true, Mode: "additive", PartSizeMB: 256, SplitActive: true,
				JobIDs: []int64{5},
			}
			s.jobs[j.ID] = j

			ref := s.ResumeJob(j.ID)

			if ref.Err == nil {
				t.Fatal("ResumeJob resumed a split job whose plan cannot be rebuilt")
			}
			if !strings.Contains(ref.Err.Detail, "Start a new backup") {
				t.Fatalf("detail = %q, want a start-a-new-backup instruction", ref.Err.Detail)
			}
			if got := atomic.LoadInt32(&calls); got != 0 {
				t.Fatalf("rclone was called %d times for an unrebuildable split job, want 0", got)
			}
			s.mu.Lock()
			state, ids := j.State, append([]int64(nil), j.JobIDs...)
			s.mu.Unlock()
			if state != "paused" {
				t.Fatalf("job state = %q after the refusal, want paused", state)
			}
			if len(ids) != 1 || ids[0] != 5 {
				t.Fatalf("job ids = %v after the refusal, want the original [5]", ids)
			}
		})
	}
}

// TestResumeJobRestartsSplitPipeline proves ResumeJob re-enters the split
// pipeline (pendingSplitJobID, no plain sync/copy) instead of uploading the
// large file whole. The split goroutine is allowed to fail at operations/mkdir
// so the test stays bounded; the point is which path was started.
func TestResumeJobRestartsSplitPipeline(t *testing.T) {
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

	var syncCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/copy" || r.URL.Path == "/operations/copyfile" {
			atomic.AddInt32(&syncCalls, 1)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "boom", "status": 500})
	}))
	defer server.Close()

	s := &Service{
		eng:   &serviceTestEngine{client: rc.New(server.URL, "", "", nil)},
		store: newServiceStore(t),
		jobs:  make(map[string]*jobState),
	}
	j := &jobState{
		ID: "job-split", Group: "gui-job-split", Kind: "backup", State: "paused",
		Sources: []string{src}, DestRoot: "backup", AccountID: "acct",
		FastFiles: true, Mode: "additive", PartSizeMB: 256, SplitActive: true,
		JobIDs: []int64{5},
	}
	s.jobs[j.ID] = j

	ref := s.ResumeJob(j.ID)
	if ref.Err != nil {
		t.Fatalf("ResumeJob: %+v", ref.Err)
	}

	s.mu.Lock()
	ids := append([]int64(nil), j.JobIDs...)
	s.mu.Unlock()
	found := false
	for _, id := range ids {
		if id == pendingSplitJobID {
			found = true
		}
	}
	if !found {
		t.Fatalf("job ids = %v, want the pending split marker %d", ids, pendingSplitJobID)
	}
	if syncCalls != 0 {
		t.Fatalf("plain sync/copy jobs started (%d), want the split pipeline only", syncCalls)
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

// TestActiveJobsReportsFinishedJob keeps the ActiveJobs contract covered after
// its job-state updates moved under s.mu.
func TestActiveJobsReportsFinishedJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"bytes": 123, "totalBytes": 500, "speed": 10.5, "errors": 0,
			})
		case "/job/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "finished": true, "success": true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{
		eng:   &serviceTestEngine{client: rc.New(server.URL, "", "", nil)},
		store: newServiceStore(t),
		jobs:  make(map[string]*jobState),
	}
	s.jobs["job-1"] = &jobState{
		ID: "job-1", Group: "gui-job-1", JobIDs: []int64{1},
		Kind: "upload", State: "running", Title: "files", StartedAt: time.Now(),
	}

	list := s.ActiveJobs()

	if list.Err != nil {
		t.Fatalf("ActiveJobs: %+v", list.Err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Items))
	}
	item := list.Items[0]
	if item.State != "done" {
		t.Fatalf("state = %q, want done", item.State)
	}
	if item.Bytes != 123 || item.TotalBytes != 500 {
		t.Fatalf("bytes = %d/%d, want 123/500", item.Bytes, item.TotalBytes)
	}
	s.mu.Lock()
	records := len(s.records)
	s.mu.Unlock()
	if records != 1 {
		t.Fatalf("history records = %d, want 1", records)
	}
}

// ---------------------------------------------------------------- liveness

// jobStatusServer answers job/status with a fixed reply and serves the empty
// stats call ActiveJobs makes first.
func jobStatusServer(t *testing.T, code int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/stats":
			_, _ = w.Write([]byte("{}"))
		case "/job/status":
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestReconcileAfterRestartCoversAllJobKinds proves a restart terminalises every
// non-goroutine-owned kind (not only backups), leaves split/restore/resuming
// jobs to their owners, and does not fail a job started against the new engine.
// A job from the old engine is not a proven failure: it may have completed
// before the restart, so it becomes "interrupted", never "failed".
func TestReconcileAfterRestartCoversAllJobKinds(t *testing.T) {
	// The old engine's ids are gone; the new engine's job is still running.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/stats":
			_, _ = w.Write([]byte("{}"))
		case "/job/status":
			var in struct {
				JobID int64 `json:"jobid"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.JobID == 3 {
				_, _ = w.Write([]byte(`{"id":3,"finished":false}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"job not found","status":500}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady, Since: time.Now().Add(-time.Minute)},
	}
	s := &Service{eng: eng, jobs: make(map[string]*jobState)}
	old := time.Now().Add(-time.Hour)
	for _, kind := range []string{"backup", "upload", "download", "copy"} {
		id := "old-" + kind
		s.jobs[id] = &jobState{
			ID: id, Group: "gui-" + id, Kind: kind, State: "running",
			JobIDs: []int64{1}, StartedAt: old,
		}
	}
	s.jobs["old-split"] = &jobState{
		ID: "old-split", Group: "gui-old-split", Kind: "backup", State: "running",
		SplitRunning: true, JobIDs: []int64{pendingSplitJobID}, StartedAt: old,
	}
	s.jobs["old-restore"] = &jobState{
		ID: "old-restore", Group: "gui-old-restore", Kind: "restore", State: "running",
		JobIDs: []int64{2}, StartedAt: old,
	}
	s.jobs["old-resuming"] = &jobState{
		ID: "old-resuming", Group: "gui-old-resuming", Kind: "backup", State: "running",
		Resuming: true, JobIDs: []int64{4}, StartedAt: old,
	}
	s.jobs["new-upload"] = &jobState{
		ID: "new-upload", Group: "gui-new-upload", Kind: "upload", State: "running",
		JobIDs: []int64{3}, StartedAt: time.Now(),
	}

	s.reconcileAfterRestart(context.Background())

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, kind := range []string{"backup", "upload", "download", "copy"} {
		if got := s.jobs["old-"+kind].State; got != "interrupted" {
			t.Fatalf("%s job state = %q, want interrupted after the engine restart", kind, got)
		}
		if msg := s.jobs["old-"+kind].Err; !strings.Contains(msg, "may have completed") {
			t.Fatalf("%s job error = %q, want a clear unobserved-outcome explanation", kind, msg)
		}
	}
	if got := s.jobs["old-split"].State; got != "running" {
		t.Fatalf("split job state = %q, want running (its goroutine owns it)", got)
	}
	if got := s.jobs["old-restore"].State; got != "running" {
		t.Fatalf("restore job state = %q, want running (its goroutine owns it)", got)
	}
	if got := s.jobs["old-resuming"].State; got != "running" {
		t.Fatalf("resuming job state = %q, want running (ResumeJob owns it)", got)
	}
	if got := s.jobs["new-upload"].State; got == "failed" || got == "interrupted" {
		t.Fatalf("upload started against the new engine was terminalised by the restart: %q", got)
	}
}

// TestActiveJobsExpiredJobIsNotRecordedAsDone covers finding 2: after rclone
// purges a job's id, ActiveJobs must finalise the phantom running transfer, but
// an expired id does not prove the run succeeded (rclone expires failed jobs
// too), so the outcome is recorded as interrupted with an explanation.
func TestActiveJobsExpiredJobIsNotRecordedAsDone(t *testing.T) {
	server := jobStatusServer(t, http.StatusInternalServerError,
		`{"error":"job not found","status":500}`)
	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady, Since: time.Now().Add(-time.Hour)},
	}
	s := &Service{eng: eng, jobs: make(map[string]*jobState)}
	s.jobs["job-1"] = &jobState{
		ID: "job-1", Group: "gui-job-1", JobIDs: []int64{1},
		Kind: "upload", State: "running", StartedAt: time.Now().Add(-30 * time.Minute),
	}

	list := s.ActiveJobs()

	if len(list.Items) != 1 || list.Items[0].State != "interrupted" {
		t.Fatalf("items = %+v, want one interrupted job, not a false success", list.Items)
	}
	if list.Items[0].Error == "" {
		t.Fatal("interrupted job has no explanation of the unobserved outcome")
	}
	s.mu.Lock()
	records := append([]dto.Record(nil), s.records...)
	s.mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("history records = %d, want 1", len(records))
	}
	if records[0].State != "interrupted" || records[0].Error == "" {
		t.Fatalf("history = %+v, want one interrupted record with an explanation", records[0])
	}
}

// TestActiveJobsProbeErrorKeepsJobRunning pins that an unanswerable probe never
// terminalises a transfer.
func TestActiveJobsProbeErrorKeepsJobRunning(t *testing.T) {
	server := jobStatusServer(t, http.StatusInternalServerError,
		`{"error":"internal error","status":500}`)
	eng := &serviceTestEngine{client: rc.New(server.URL, "", "", nil)}
	s := &Service{eng: eng, jobs: make(map[string]*jobState)}
	s.jobs["job-1"] = &jobState{
		ID: "job-1", Group: "gui-job-1", JobIDs: []int64{1},
		Kind: "upload", State: "running", StartedAt: time.Now(),
	}

	list := s.ActiveJobs()

	if len(list.Items) != 1 || list.Items[0].State != "running" {
		t.Fatalf("items = %+v, want the job still running", list.Items)
	}
	s.mu.Lock()
	records := len(s.records)
	s.mu.Unlock()
	if records != 0 {
		t.Fatalf("history records = %d, want 0", records)
	}
}

// TestActiveJobsOldEngineJobIsNotRecordedAsFailed covers finding 4: a job from
// an earlier engine whose ids are gone may have completed before the restart,
// so it must not be written to History as failed.
func TestActiveJobsOldEngineJobIsNotRecordedAsFailed(t *testing.T) {
	server := jobStatusServer(t, http.StatusInternalServerError,
		`{"error":"job not found","status":500}`)
	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady, Since: time.Now().Add(-time.Minute)},
	}
	s := &Service{eng: eng, jobs: make(map[string]*jobState)}
	s.jobs["job-1"] = &jobState{
		ID: "job-1", Group: "gui-job-1", JobIDs: []int64{1},
		Kind: "upload", State: "running", StartedAt: time.Now().Add(-time.Hour),
	}

	list := s.ActiveJobs()

	if len(list.Items) != 1 || list.Items[0].State != "interrupted" {
		t.Fatalf("items = %+v, want interrupted: a restart does not prove the run failed", list.Items)
	}
	if !strings.Contains(list.Items[0].Error, "may have completed") {
		t.Fatalf("error = %q, want the may-have-completed explanation", list.Items[0].Error)
	}
}

// TestActiveJobsIgnoresRecycledJobID pins the group check: a numeric id which
// now belongs to a different job must not lend its (failed) result to the
// original transfer. The original's outcome is unknown, so it is interrupted
// rather than failed.
func TestActiveJobsIgnoresRecycledJobID(t *testing.T) {
	server := jobStatusServer(t, http.StatusOK,
		`{"id":1,"finished":true,"success":false,"error":"unrelated job failed","group":"gui-other"}`)
	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady, Since: time.Now().Add(-time.Hour)},
	}
	s := &Service{eng: eng, jobs: make(map[string]*jobState)}
	s.jobs["job-1"] = &jobState{
		ID: "job-1", Group: "gui-job-1", JobIDs: []int64{1},
		Kind: "upload", State: "running", StartedAt: time.Now().Add(-30 * time.Minute),
	}

	list := s.ActiveJobs()

	if len(list.Items) != 1 || list.Items[0].State != "interrupted" {
		t.Fatalf("items = %+v, want the original job interrupted, not failed from a recycled id", list.Items)
	}
	if strings.Contains(list.Items[0].Error, "unrelated job failed") {
		t.Fatalf("error = %q, leaked the recycled job's failure", list.Items[0].Error)
	}
}

// TestActiveJobsLeavesResumingJobInFlight covers finding 1: ResumeJob reserves a
// job by flipping it to "running" while its cached ids still belong to the
// stopped run. A probe during that window says nothing about the new run, so
// ActiveJobs must not terminalise the job and markJobTerminal must refuse.
func TestActiveJobsLeavesResumingJobInFlight(t *testing.T) {
	var probes int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/stats":
			_, _ = w.Write([]byte("{}"))
		case "/job/status":
			atomic.AddInt32(&probes, 1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"job not found","status":500}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady, Since: time.Now().Add(-time.Hour)},
	}
	s := &Service{eng: eng, jobs: make(map[string]*jobState)}
	s.jobs["job-1"] = &jobState{
		ID: "job-1", Group: "gui-job-1", JobIDs: []int64{1},
		Kind: "upload", State: "running", Resuming: true, StartedAt: time.Now(),
	}

	list := s.ActiveJobs()

	if len(list.Items) != 1 || list.Items[0].State != "running" {
		t.Fatalf("items = %+v, want the reserved job still running", list.Items)
	}
	if got := atomic.LoadInt32(&probes); got != 0 {
		t.Fatalf("job/status probes = %d, want 0 while ResumeJob owns the job", got)
	}
	s.mu.Lock()
	records := len(s.records)
	state := s.jobs["job-1"].State
	s.mu.Unlock()
	if records != 0 {
		t.Fatalf("history records = %d, want 0", records)
	}
	if state != "running" {
		t.Fatalf("job state = %q, want running", state)
	}
	if s.markJobTerminal("job-1", "done", "", []int64{1}) {
		t.Fatal("markJobTerminal terminalised a job which ResumeJob is reserving")
	}
}

// TestActiveTransfersUsesLiveness covers finding 5: the close guard must not
// trust the cached State alone. A job whose ids finished or expired no longer
// transfers, while a paused/resuming/split job and an unanswerable probe still
// count.
func TestActiveTransfersUsesLiveness(t *testing.T) {
	live := func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"id":1,"finished":false}`))
	}
	finished := func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"id":1,"finished":true,"success":true}`))
	}
	expired := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"job not found","status":500}`))
	}
	unanswerable := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error","status":500}`))
	}

	tests := []struct {
		name  string
		job   jobState
		reply func(http.ResponseWriter)
		want  int
	}{
		{name: "paused job", job: jobState{State: "paused"}, reply: live, want: 1},
		{name: "resuming job", job: jobState{State: "running", Resuming: true}, reply: expired, want: 1},
		{name: "split goroutine", job: jobState{State: "running", SplitRunning: true}, reply: expired, want: 1},
		{name: "live transfer", job: jobState{State: "running", JobIDs: []int64{1}}, reply: live, want: 1},
		{name: "unanswerable probe", job: jobState{State: "running", JobIDs: []int64{1}}, reply: unanswerable, want: 1},
		{name: "finished transfer", job: jobState{State: "running", JobIDs: []int64{1}}, reply: finished, want: 0},
		{name: "expired transfer", job: jobState{State: "running", JobIDs: []int64{1}}, reply: expired, want: 0},
		{name: "done job", job: jobState{State: "done"}, reply: live, want: 0},
		{name: "failed job", job: jobState{State: "failed"}, reply: live, want: 0},
		{name: "interrupted job", job: jobState{State: "interrupted"}, reply: live, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/core/stats":
					_, _ = w.Write([]byte("{}"))
				case "/job/status":
					tc.reply(w)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			eng := &serviceTestEngine{
				client: rc.New(server.URL, "", "", nil),
				status: engine.Status{State: engine.StateReady, Since: time.Now().Add(-time.Hour)},
			}
			s := &Service{eng: eng, jobs: make(map[string]*jobState)}
			job := tc.job
			job.ID = "job-1"
			job.Group = "gui-job-1"
			job.Kind = "backup"
			job.StartedAt = time.Now()
			s.jobs[job.ID] = &job

			if got := ActiveTransfers(s); got != tc.want {
				t.Fatalf("ActiveTransfers = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestRestartEngineQueuedCallGetsFreshContext reproduces finding 5: a restart
// queued behind a slow one arrives with an already-expired caller context and
// must still get a fresh deadline instead of stopping the engine.
func TestRestartEngineQueuedCallGetsFreshContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mount/listmounts":
			_, _ = w.Write([]byte(`{"mountPoints":[]}`))
		case "/mount/unmount", "/mount/mount":
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady},
	}
	eng.onRestart = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	s := &Service{eng: eng}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.restartEngineWithMounts(context.Background())
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first restart did not reach the engine")
	}

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	var queuedErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		queuedErr = s.restartEngineWithMounts(expired)
	}()
	// Give the queued call time to reach the lock before releasing the first.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if queuedErr != nil {
		t.Fatalf("queued restart failed with an expired caller context: %v", queuedErr)
	}
	if eng.restarts != 2 {
		t.Fatalf("engine restarts = %d, want 2 (the queued request must still run)", eng.restarts)
	}
}

// TestRestartEngineGetsFreshContextAfterSlowUnmount reproduces finding 3: the
// restart deadline used to start before detachMounts, so a slow unmount could
// consume the whole budget and hand eng.Restart an expired context (which kills
// rclone and fails to start a replacement). The restart must get a fresh clock.
func TestRestartEngineGetsFreshContextAfterSlowUnmount(t *testing.T) {
	old := restartTimeout
	restartTimeout = 100 * time.Millisecond
	defer func() { restartTimeout = old }()

	var unmounts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mount/listmounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"mountPoints": []map[string]any{
				{"Fs": "gdrive:Photos", "MountPoint": "X:"},
			}})
		case "/mount/unmount":
			atomic.AddInt32(&unmounts, 1)
			// Slower than the whole restart budget: only a deadline created
			// after the detach can still be alive here.
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte("{}"))
		case "/mount/mount":
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady},
	}
	s := &Service{eng: eng}

	if err := s.restartEngineWithMounts(context.Background()); err != nil {
		t.Fatalf("restartEngineWithMounts: %v", err)
	}
	if eng.restarts != 1 {
		t.Fatalf("engine restarts = %d, want 1", eng.restarts)
	}
	if eng.restartCtxErr != nil {
		t.Fatalf("engine restarted with an expired context (%v); a slow unmount consumed the budget", eng.restartCtxErr)
	}
	if got := atomic.LoadInt32(&unmounts); got != 1 {
		t.Fatalf("unmounts = %d, want 1", got)
	}
}

// TestRestartEngineFailureIsVisible proves a restart aborted because the mounts
// could not be listed is reported to the caller and recorded in History.
func TestRestartEngineFailureIsVisible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mount/listmounts" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom","status":500}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady},
	}
	s := &Service{eng: eng, jobs: make(map[string]*jobState)}

	out := s.RestartEngine()
	if out.Err == nil {
		t.Fatal("RestartEngine reported success although the restart was aborted")
	}
	if eng.restarts != 0 {
		t.Fatalf("engine restarts = %d, want 0", eng.restarts)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, rec := range s.records {
		if rec.Kind == "settings" && rec.State == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restart failure is not visible in History: %+v", s.records)
	}
}

// TestSaveSettingsRecordsRestartFailure covers the asynchronous path: SaveSettings
// has already told the UI "saved", so an aborted restart must still become
// visible instead of silently leaving the new setting inactive.
func TestSaveSettingsRecordsRestartFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mount/listmounts" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom","status":500}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	eng := &serviceTestEngine{
		client: rc.New(server.URL, "", "", nil),
		status: engine.Status{State: engine.StateReady},
	}
	s := &Service{eng: eng, store: newServiceStore(t), jobs: make(map[string]*jobState)}

	in := dto.Settings{}
	in.Advanced.Proxy = true
	s.SaveSettings(in)

	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		found := false
		for _, rec := range s.records {
			if rec.Kind == "settings" && rec.State == "failed" {
				found = true
			}
		}
		s.mu.Unlock()
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a failed restart after SaveSettings was never made visible")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eng.restarts != 0 {
		t.Fatalf("engine restarts = %d, want 0", eng.restarts)
	}
}

// TestFinishJobReplacesPreviousAttemptRecord pins the retry behaviour: ResumeJob
// reuses the job id, so a successful retry must replace the earlier failure in
// History instead of being dropped as a duplicate.
func TestFinishJobReplacesPreviousAttemptRecord(t *testing.T) {
	s := &Service{jobs: make(map[string]*jobState)}
	j := &jobState{ID: "job-retry", Kind: "backup", Title: "docs", State: "failed", Err: "boom"}
	j.StartedAt = time.Unix(100, 0)
	s.finishJob(j)

	// The user retries; the same id now finishes successfully.
	j.State = "done"
	j.Err = ""
	j.Files = 3
	j.Bytes = 42
	j.StartedAt = time.Unix(200, 0)
	s.finishJob(j)

	s.mu.Lock()
	records := append([]dto.Record(nil), s.records...)
	s.mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1 (one row per job)", len(records))
	}
	rec := records[0]
	if rec.State != "done" || rec.Files != 3 || rec.Bytes != 42 || rec.Error != "" {
		t.Fatalf("record = %#v, want the retried attempt's outcome", rec)
	}
	if rec.StartedAt != 200 {
		t.Fatalf("startedAt = %d, want the retry's start", rec.StartedAt)
	}
}
