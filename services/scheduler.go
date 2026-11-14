package services

// Scheduled backups.
//
// StartScheduler is a package function rather than a Service method so Wails
// does not expose the loop to the frontend. It owns one goroutine which wakes
// up every 30 seconds, compares the saved schedule with its LastRun and starts
// StartBackup once per scheduled point.

import (
	"context"
	"strings"
	"time"

	"cloudnest/internal/dto"
)

const (
	schedulerWarmup = 2 * time.Second
	schedulerTick   = 30 * time.Second
)

// schedulerState lives only inside the scheduler goroutine. lastPoint remembers
// the most recent scheduled point an attempt was made for, which keeps a
// failed settings write from causing a second backup for the same point.
type schedulerState struct {
	lastPoint int64
	running   bool
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
	state.running = true
	defer func() { state.running = false }()

	dest := strings.TrimSpace(sch.DestRoot)
	if dest == "" {
		dest = "電腦備份"
	}
	mode := strings.TrimSpace(sch.Mode)
	if mode == "" {
		mode = "additive"
	}

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
		s.schedulerWarn("scheduler: backup did not start",
			"code", ref.Err.Code, "message", ref.Err.Message, "detail", ref.Err.Detail)
		// Leave lastPoint untouched: LastRun is still behind the scheduled
		// point, so the next tick retries (for example once the engine is up).
		return
	}

	state.lastPoint = point.Unix()
	if err := s.store.Update(func(cur *dto.Settings) {
		cur.Schedule.LastRun = time.Now().Unix()
	}); err != nil {
		s.schedulerWarn("scheduler: save last run failed", "err", err)
	}
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
