package services

import (
	"context"
	"time"

	"cloudnest/internal/rc"
)

// Close guard helpers.
//
// The window close hook needs to know whether a transfer is still running and
// whether the user already confirmed stopping it. These are package functions
// rather than Service methods so the Wails binding generator does not expose
// them to the frontend.

// ActiveTransfers counts the jobs the user would want a warning about.
//
// The cached jobState.State is not a liveness signal: only ActiveJobs advances
// it, and that is driven by the frontend, so a transfer which finished while
// the user was on another page would keep its cached "running" forever and
// block a close for a job which no longer exists. The liveness decision is
// therefore made here too:
//
//   - paused and resuming jobs count (the user still owns them);
//   - a split goroutine counts until it clears SplitRunning;
//   - a running job is confirmed against rclone: live or unanswerable probes
//     count, while ids which finished or were expired/purged do not;
//   - a probe which cannot run at all (no engine client) counts, because
//     stopping an unknown transfer silently is worse than one extra prompt.
func ActiveTransfers(s *Service) int {
	if s == nil {
		return 0
	}
	type candidate struct {
		group        string
		kind         string
		state        string
		splitRunning bool
		resuming     bool
		ids          []int64
		startedAt    time.Time
	}
	s.mu.Lock()
	cands := make([]candidate, 0, len(s.jobs))
	for _, j := range s.jobs {
		cands = append(cands, candidate{
			group: j.Group, kind: j.Kind, state: j.State,
			splitRunning: j.SplitRunning, resuming: j.Resuming,
			ids: append([]int64(nil), j.JobIDs...), startedAt: j.StartedAt,
		})
	}
	s.mu.Unlock()

	var c *rc.Client
	probeOK := false
	if s.eng != nil {
		if client, fail := s.client(); fail == nil {
			c, probeOK = client, true
		}
	}
	epoch := s.engineEpoch()
	// One shared deadline for the whole close check: the close hook must answer
	// quickly, and a wedged daemon makes every remaining probe unknown (counted).
	ctx, cancel := context.WithTimeout(context.Background(), schedulerProbeTimeout)
	defer cancel()
	n := 0
	for _, j := range cands {
		switch j.state {
		case "paused":
			n++
			continue
		case "running":
		default:
			continue
		}
		// These flags are live decisions which do not need (and must not use)
		// a probe: a split goroutine is still running, and a resume owns the
		// job until it installs the new ids.
		if j.splitRunning || j.resuming || j.kind == "restore" {
			n++
			continue
		}
		if !probeOK {
			n++
			continue
		}
		verdict, _ := probeJobLiveness(c, ctx, j.group, j.ids, j.startedAt, epoch, j.resuming)
		switch verdict {
		case verdictLive, verdictUnknown:
			n++
		}
	}
	return n
}

// AllowClose records that the user accepted stopping the transfers.
func AllowClose(s *Service) { s.closing.Store(true) }

// CloseConfirmed reports whether AllowClose has been called.
func CloseConfirmed(s *Service) bool { return s.closing.Load() }

// UnmountAll detaches every rclone mount before the app exits. rclone hosts the
// mount inside its own process, so quitting without this can leave a drive
// letter pointing at nothing until the next logon.
func UnmountAll(s *Service) {
	if s == nil {
		return
	}
	list := s.ListMounts()
	for _, m := range list.Items {
		_ = s.UnmountCloud(m.MountPoint)
	}
}
