package services

// Scheduled backups.
//
// StartScheduler is a package function rather than a Service method so Wails
// does not expose the loop to the frontend. It owns one goroutine which wakes
// up every 30 seconds, compares the saved schedule with its LastRun and starts
// StartBackup once per scheduled point.

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cloudnest/internal/dto"
)

const (
	schedulerWarmup = 2 * time.Second
	schedulerTick   = 30 * time.Second

	// A start that fails is retried with exponential backoff instead of on
	// every tick, so a permanent failure (a missing source or a revoked token)
	// cannot create another rclone job every 30 seconds. The backoff is capped
	// at schedulerRetryMax and keeps retrying for the rest of the day: a source
	// or engine which comes back later must still get its backup. A visible
	// history entry is recorded once when the long backoff begins.
	schedulerRetryBase   = 30 * time.Second
	schedulerRetryMax    = 15 * time.Minute
	schedulerMaxAttempts = 5
)

// schedulerProbeTimeout bounds each job/status probe the liveness checks make.
// The engine is local, so an unknown job id is answered at once; the timeout
// only protects the caller from a wedged daemon and must never be read as "the
// job is gone". It is a variable only so tests can shorten it.
var schedulerProbeTimeout = 2 * time.Second

// schedulerState lives only inside the scheduler goroutine. lastPoint remembers
// the most recent scheduled point an attempt was made for, which keeps a
// failed settings write from causing a second backup for the same point.
type schedulerState struct {
	lastPoint int64
	running   bool

	// failPoint is the point whose starts are failing, attempts counts the
	// consecutive failures for it and nextAttempt is the earliest time the
	// next attempt is allowed. reported records that the long-backoff failure
	// has already been written to history, so it is not written every retry.
	failPoint   int64
	attempts    int
	nextAttempt time.Time
	reported    bool

	// blockedPoint/blockedJob keep an overlapping in-flight backup from
	// logging a warning on every tick while it is still holding the point.
	blockedPoint int64
	blockedJob   string
}

// attemptAllowed reports whether the backoff window for point has elapsed.
func (st *schedulerState) attemptAllowed(point int64, now time.Time) bool {
	if st.failPoint != point || st.nextAttempt.IsZero() {
		return true
	}
	return !now.Before(st.nextAttempt)
}

// recordFailure counts a failed start for point and schedules the next attempt
// with exponential backoff, capped at schedulerRetryMax. report is true exactly
// once per point, on the failure which begins the long backoff, so the caller
// can write a single visible history entry instead of one on every retry.
func (st *schedulerState) recordFailure(point int64, now time.Time) (report bool) {
	if st.failPoint != point {
		st.failPoint = point
		st.attempts = 0
		st.reported = false
	}
	st.attempts++
	// 30s, 60s, 120s, 240s, 8m, then the 15-minute cap. Shifting above six
	// attempts would overflow the duration, so stop doubling there.
	backoff := schedulerRetryMax
	if st.attempts < 6 {
		backoff = schedulerRetryBase << (st.attempts - 1)
		if backoff > schedulerRetryMax {
			backoff = schedulerRetryMax
		}
	}
	st.nextAttempt = now.Add(backoff)
	if st.attempts >= schedulerMaxAttempts && !st.reported {
		st.reported = true
		return true
	}
	return false
}

// markAttempted records that point has been started successfully and clears any
// backoff state. A failed point is deliberately NOT marked: only a successful
// start retires the point, so a source or engine which comes back later the
// same day is still retried, and the next scheduled point simply supersedes it.
func (st *schedulerState) markAttempted(point int64) {
	st.lastPoint = point
	st.failPoint = 0
	st.attempts = 0
	st.nextAttempt = time.Time{}
	st.reported = false
}

// StartScheduler starts the automatic-backup loop. It returns immediately; the
// background goroutine exits when ctx is cancelled.
func StartScheduler(s *Service, ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	go func() {
		state := &schedulerState{}

		// Give rclone a moment to finish starting before the first evaluation:
		// a scheduled run at launch would otherwise race the engine.
		warmup := time.NewTimer(schedulerWarmup)
		defer warmup.Stop()
		select {
		case <-ctx.Done():
			return
		case <-warmup.C:
			s.runScheduleSafe(state)
		}

		ticker := time.NewTicker(schedulerTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runScheduleSafe(state)
			}
		}
	}()
}

// runScheduleSafe isolates one evaluation so a bug in the plan or in rclone
// handling can never take the scheduler down.
func (s *Service) runScheduleSafe(state *schedulerState) {
	defer func() {
		if r := recover(); r != nil {
			s.schedulerWarn("scheduler: recovered from panic", "panic", r)
		}
	}()
	s.evaluateSchedule(state)
}

// evaluateSchedule starts the scheduled backup when it is due and has not
// already been started for the current scheduled point.
func (s *Service) evaluateSchedule(state *schedulerState) {
	if s == nil || s.store == nil || state == nil {
		return
	}
	now := time.Now()
	sch := s.store.Get().Schedule
	if !scheduleDue(sch, now) {
		return
	}
	point, ok := lastScheduledPoint(sch.Time, now)
	if !ok {
		return
	}
	// A previous attempt for this point succeeded but settings could not be
	// written; do not start a second backup because of that.
	if state.lastPoint >= point.Unix() {
		return
	}
	if state.running {
		return
	}
	if !state.attemptAllowed(point.Unix(), now) {
		return
	}

	dest := strings.TrimSpace(sch.DestRoot)
	if dest == "" {
		dest = "電腦備份"
	}
	mode := strings.TrimSpace(sch.Mode)
	if mode == "" {
		mode = "additive"
	}

	// state.running only covers this function: StartBackup returns as soon as
	// the rclone job is registered, so a transfer from an earlier point (or a
	// manual backup) may still be running or paused. Starting another one would
	// sync the same source concurrently and re-hash the same local files.
	if block, busy := s.overlappingBackup(sch.AccountID, dest, sch.SourcePaths); busy {
		if state.blockedPoint != point.Unix() || state.blockedJob != block.id {
			state.blockedPoint = point.Unix()
			state.blockedJob = block.id
			s.schedulerWarn("scheduler: backup already in progress",
				"job", block.id, "paused", block.paused, "point", point.Format(time.RFC3339))
		}
		// A paused backup holds this point for as long as it stays paused, and
		// nothing else in the UI says automatic backups have stopped. Record one
		// visible History entry per scheduled point instead of only a log line.
		if block.paused {
			s.recordScheduledBlocked(point, block.id)
		}
		return
	}
	state.blockedPoint, state.blockedJob = 0, ""

	state.running = true
	defer func() { state.running = false }()

	ref := s.StartBackup(dto.PlanRequest{
		SourcePaths: append([]string(nil), sch.SourcePaths...),
		AccountID:   sch.AccountID,
		DestRoot:    dest,
		Mode:        mode,
		FastFiles:   sch.FastFiles,
		Transfers:   s.defaultTransfers(),
		Excludes:    nil,
	})
	if ref.Err != nil {
		report := state.recordFailure(point.Unix(), now)
		s.schedulerWarn("scheduler: backup did not start",
			"code", ref.Err.Code, "message", ref.Err.Message, "detail", ref.Err.Detail,
			"attempt", state.attempts)
		if report {
			// The point stays eligible: only a successful start retires it.
			// Record one visible failure when the long backoff begins so the
			// user can see why the automatic backup has not run, then keep
			// retrying every schedulerRetryMax for the rest of the day.
			s.recordScheduledFailure(point, ref)
			s.schedulerWarn("scheduler: scheduled backup keeps failing; retrying later",
				"point", point.Format(time.RFC3339), "every", schedulerRetryMax.String())
		}
		return
	}

	state.markAttempted(point.Unix())
	if err := s.store.Update(func(cur *dto.Settings) {
		cur.Schedule.LastRun = time.Now().Unix()
	}); err != nil {
		s.schedulerWarn("scheduler: save last run failed", "err", err)
	}
}

// backupOverlap describes the job which blocks a scheduled point. paused is
// true when the blocker needs the user's attention: only a paused job can hold
// the point forever, while a running one eventually finishes.
type backupOverlap struct {
	id     string
	paused bool
}

// overlappingBackup returns the backup whose account, destination and sources
// conflict with the scheduled run. The cached jobState.State is not a liveness
// signal: only ActiveJobs advances it, and that is driven by the frontend, so a
// backup which finished while the window sat in the tray would otherwise stay
// "running" and block every later point. A running job is therefore confirmed
// against rclone; one whose rclone jobs are all finished (or expired after
// finishing) no longer blocks, while a probe which cannot answer keeps it busy.
// The job map is snapshotted under s.mu and the network probes run without it.
func (s *Service) overlappingBackup(accountID, dest string, sources []string) (backupOverlap, bool) {
	type snapshot struct {
		id           string
		group        string
		state        string
		splitRunning bool
		resuming     bool
		ids          []int64
		startedAt    time.Time
	}
	s.mu.Lock()
	candidates := make([]snapshot, 0, len(s.jobs))
	for _, j := range s.jobs {
		if !backupJobConflicts(j, accountID, dest, sources) {
			continue
		}
		candidates = append(candidates, snapshot{
			id: j.ID, group: j.Group, state: j.State, splitRunning: j.SplitRunning,
			resuming: j.Resuming,
			ids:      append([]int64(nil), j.JobIDs...), startedAt: j.StartedAt,
		})
	}
	s.mu.Unlock()

	for _, c := range candidates {
		// A paused job still owns its group and split parts and the user can
		// resume it, and a split goroutine is live until it clears the flag.
		if c.state == "paused" {
			return backupOverlap{id: c.id, paused: true}, true
		}
		// A resume in progress has already started or is about to start new
		// rclone jobs. Its cached ids still belong to the stopped run, so a
		// probe must not be consulted and the point must stay blocked.
		if c.splitRunning || c.resuming {
			return backupOverlap{id: c.id}, true
		}
		if c.state != "running" {
			continue
		}
		if s.backupJobStillRunning(c.id, c.group, c.ids, c.startedAt, c.resuming) {
			return backupOverlap{id: c.id}, true
		}
	}
	return backupOverlap{}, false
}

// backupJobStillRunning reports whether the rclone jobs behind a cached
// "running" backup are really unfinished. The engine is authoritative only when
// it answers definitively:
//
//   - a running job keeps the point blocked;
//   - a probe which times out or fails otherwise means "unknown", which is
//     treated as busy so a duplicate sync is never started;
//   - a job which is being resumed is busy by definition: its cached ids belong
//     to the stopped run, so no probe answer is about the run ResumeJob started;
//   - an id which is unknown to the engine only proves the run ended, because
//     rclone expires successful and failed jobs alike. The run is recorded as
//     interrupted (not done, not failed) and the point is unblocked.
func (s *Service) backupJobStillRunning(id, group string, ids []int64, startedAt time.Time, resuming bool) bool {
	if resuming {
		return true
	}
	c, fail := s.client()
	if fail != nil {
		// Liveness is unknown (the engine is down or restarting). Treat the
		// job as busy rather than starting a duplicate backup.
		return true
	}
	verdict, msg := probeJobLiveness(c, context.Background(), group, ids, startedAt, s.engineEpoch(), resuming)
	switch verdict {
	case verdictLive, verdictUnknown:
		return true
	case verdictFailed:
		s.markJobTerminal(id, "failed", msg, ids)
	case verdictUnobserved:
		s.markJobTerminal(id, "interrupted", msg, ids)
	default:
		// rclone definitively reported a successful finish.
		s.markJobTerminal(id, "done", "", ids)
	}
	return false
}

// recordScheduledFailure writes one visible History entry for a scheduled point
// whose starts keep failing. It is called once per point (schedulerState
// reports only the failure which begins the long backoff) and is persisted
// through the journal so it also survives a restart.
func (s *Service) recordScheduledFailure(point time.Time, ref dto.JobRef) {
	sch := s.store.Get().Schedule
	title := "Scheduled backup"
	if len(sch.SourcePaths) > 0 {
		title = filepath.Base(filepath.Clean(sch.SourcePaths[0]))
	}
	msg := "scheduled backup did not start"
	if ref.Err != nil {
		msg = ref.Err.Message
		if ref.Err.Detail != "" {
			msg += ": " + ref.Err.Detail
		}
	}
	rec := dto.Record{
		ID: scheduledFailureID(point), Kind: "backup", State: "failed", Title: title,
		StartedAt: point.Unix(), FinishedAt: time.Now().Unix(), Error: msg,
	}

	s.mu.Lock()
	for _, existing := range s.records {
		if existing.ID == rec.ID {
			s.mu.Unlock()
			return
		}
	}
	s.records = append([]dto.Record{rec}, s.records...)
	if len(s.records) > 50 {
		s.records = s.records[:50]
	}
	s.mu.Unlock()

	dest := strings.TrimSpace(sch.DestRoot)
	if dest == "" {
		dest = "電腦備份"
	}
	mode := strings.TrimSpace(sch.Mode)
	if mode == "" {
		mode = "additive"
	}
	s.journalStart(rec, dto.PlanRequest{
		SourcePaths: append([]string(nil), sch.SourcePaths...),
		AccountID:   sch.AccountID,
		DestRoot:    dest,
		Mode:        mode,
		FastFiles:   sch.FastFiles,
	})
	s.journalFinish(rec)
}

// recordScheduledBlocked writes one visible History entry per scheduled point
// when a paused backup holds it. The entry is keyed by the point so a 30 s tick
// cannot add a duplicate, and it tells the user which job to resume or cancel.
// It is deliberately not journaled: the paused job lives only in memory, so the
// condition disappears when the app restarts.
func (s *Service) recordScheduledBlocked(point time.Time, jobID string) {
	rec := dto.Record{
		ID:   fmt.Sprintf("schedule-blocked-%d", point.Unix()),
		Kind: "backup", State: "failed", Title: "Scheduled backup",
		StartedAt: point.Unix(), FinishedAt: time.Now().Unix(),
		Error: fmt.Sprintf("a paused backup (%s) is blocking the scheduled backup; resume or cancel it to let the schedule run", jobID),
	}
	s.mu.Lock()
	for _, existing := range s.records {
		if existing.ID == rec.ID {
			s.mu.Unlock()
			return
		}
	}
	s.records = append([]dto.Record{rec}, s.records...)
	if len(s.records) > 50 {
		s.records = s.records[:50]
	}
	s.mu.Unlock()
}

// scheduledFailureID is the stable History id for the failure recorded for one
// scheduled point, so a retry cannot add a duplicate.
func scheduledFailureID(point time.Time) string {
	return fmt.Sprintf("schedule-%d", point.Unix())
}

// backupJobConflicts reports whether j is a backup of the same account and
// destination that shares at least one source with the scheduled run. A paused
// job still owns its rclone group and split parts, so it counts as in flight.
func backupJobConflicts(j *jobState, accountID, dest string, sources []string) bool {
	if j == nil || j.Kind != "backup" {
		return false
	}
	if j.State != "running" && j.State != "paused" {
		return false
	}
	if strings.TrimSpace(j.AccountID) != strings.TrimSpace(accountID) {
		return false
	}
	if strings.Trim(j.DestRoot, "/ ") != strings.Trim(dest, "/ ") {
		return false
	}
	for _, have := range j.Sources {
		for _, want := range sources {
			if sameBackupSource(have, want) {
				return true
			}
		}
	}
	return false
}

// sameBackupSource compares source folders the way the host filesystem does:
// cleaned paths, and case-insensitive on Windows.
func sameBackupSource(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == b {
		return true
	}
	return runtime.GOOS == "windows" && strings.EqualFold(a, b)
}

// scheduleDue reports whether the most recent scheduled point has not been run
// yet. It is deliberately pure so it can be tested without a Service.
func scheduleDue(sch dto.Schedule, now time.Time) bool {
	if !sch.Enabled || len(sch.SourcePaths) == 0 {
		return false
	}
	point, ok := lastScheduledPoint(sch.Time, now)
	if !ok {
		return false
	}
	return sch.LastRun < point.Unix()
}

// lastScheduledPoint returns the latest occurrence of a daily "HH:MM" time in
// now's location: today's occurrence when it has already passed, otherwise
// yesterday's. ok is false for an empty or malformed time.
func lastScheduledPoint(value string, now time.Time) (time.Time, bool) {
	clock, err := time.ParseInLocation("15:04", strings.TrimSpace(value), now.Location())
	if err != nil {
		return time.Time{}, false
	}
	point := time.Date(now.Year(), now.Month(), now.Day(),
		clock.Hour(), clock.Minute(), 0, 0, now.Location())
	if point.After(now) {
		point = point.AddDate(0, 0, -1)
	}
	return point, true
}

func (s *Service) schedulerWarn(msg string, args ...any) {
	if s != nil && s.log != nil {
		s.log.Warn(msg, args...)
	}
}
