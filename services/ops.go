package services

// File operations (delete, upload, download, split inspection and resume)
// live in this file so the existing service surface stays untouched. Every
// method follows the same contract as service.go: it returns a DTO, puts
// expected failures in Err via failFrom, and records jobs in s.jobs.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cloudnest/internal/apperr"
	"cloudnest/internal/dto"
	"cloudnest/internal/planner"
	"cloudnest/internal/rc"
	"cloudnest/internal/split"
)

const (
	// opsTimeout bounds each RC call made by a file operation. Individual
	// calls are independent so one slow entry cannot consume the budget of
	// the whole batch.
	opsTimeout = 25 * time.Second
	// opsDefaultTransfers is used when settings do not specify a value.
	opsDefaultTransfers = 4
	// manifestName is the completion marker inside a .cloudbox-part folder.
	manifestName = "manifest.json"
	// maxManifestBytes mirrors split's own limit so a stray cat response
	// cannot be decoded into the process without bound.
	maxManifestBytes = 64 * 1024
)

var opsPartNameRE = regexp.MustCompile(`^part-\d{4}\.bin$`)

// DeleteCloud removes files and folders under accountID. Each path is probed
// with operations/stat: files are deleted individually, folders (and paths
// which no longer exist) are purged. A trailing .cloudbox-part always selects
// a folder purge. Paths are relative to the account prefix, and values which
// could escape it (a remote separator or "..") are rejected.
func (s *Service) DeleteCloud(accountID string, paths []string) dto.DeleteResult {
	var out dto.DeleteResult
	fs, err := accountFS(accountID)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	c, fail := s.client()
	if fail != nil {
		out.Err = fail
		return out
	}

	var firstErr error
	for _, raw := range paths {
		remote, err := cleanRemotePath(raw)
		if err != nil {
			out.Failed = append(out.Failed, raw)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := deleteCloudPath(c, fs, remote); err != nil {
			out.Failed = append(out.Failed, raw)
			if firstErr == nil {
				firstErr = err
			}
			s.log.Warn("service: delete failed", "account", accountID, "path", remote, "err", err)
			continue
		}
		out.Deleted++
	}
	if firstErr != nil {
		out.Err = failFrom(firstErr)
	}
	return out
}

// deleteCloudPath removes a single, already sanitised remote path.
func deleteCloudPath(c *rc.Client, fs, remote string) error {
	if isSplitDirPath(remote) {
		return purgeRemote(c, fs, remote)
	}
	st, err := statRemote(c, fs, remote)
	if err != nil {
		// Some backends report a missing directory as a stat error rather
		// than item:null. Treat that as "purge the folder", exactly like the
		// explicit nil case below.
		if rc.IsNotFound(err) {
			return purgeRemote(c, fs, remote)
		}
		return err
	}
	if st == nil || st.IsDir {
		return purgeRemote(c, fs, remote)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
	defer cancel()
	return c.DeleteFile(ctx, fs, remote)
}

// Upload starts one sync/copy job per local source. Folder contents are placed
// under destPath/<basename>; a single file is placed directly in destPath.
// All jobs share a group and are registered as one upload job.
func (s *Service) Upload(accountID, destPath string, localPaths []string) dto.JobRef {
	c, fail := s.client()
	if fail != nil {
		return dto.JobRef{Err: fail}
	}
	if len(localPaths) == 0 {
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "no source selected"}}
	}
	fs, err := accountFS(accountID)
	if err != nil {
		return dto.JobRef{Err: failFrom(err)}
	}
	dest, err := cleanRemotePathAllowEmpty(destPath)
	if err != nil {
		return dto.JobRef{Err: failFrom(err)}
	}

	type source struct {
		path  string
		base  string
		isDir bool
	}
	srcs := make([]source, 0, len(localPaths))
	sources := make([]string, 0, len(localPaths))
	for _, raw := range localPaths {
		p := strings.TrimSpace(raw)
		st, err := os.Stat(p)
		if err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		srcs = append(srcs, source{
			path:  p,
			base:  filepath.Base(filepath.Clean(p)),
			isDir: st.IsDir(),
		})
		sources = append(sources, p)
	}

	id := fmt.Sprintf("job-%d", time.Now().UnixNano())
	group := "gui-" + id
	job := &jobState{
		ID: id, Group: group, Kind: "upload", State: "running",
		StartedAt: time.Now(), DestRoot: dest, AccountID: accountID,
		Sources: sources,
	}
	if len(srcs) > 0 {
		job.Title = srcs[0].base
	}
	transfers := s.defaultTransfers()
	for _, src := range srcs {
		target := dest
		if src.isDir {
			target = joinRemotePath(dest, src.base)
		}
		ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
		var jobID int64
		var err error
		if src.isDir {
			jobID, err = c.StartSync(ctx, "sync/copy", rc.SyncIn{
				SrcFs: src.path, DstFs: fs + target, Group: group, Transfers: transfers,
				Retries: s.retries(),
			})
		} else {
			// sync/copy rejects a file as srcFs; a single file is copied by
			// operations/copyfile instead.
			jobID, err = c.StartCopyFile(ctx, rc.CopyFileIn{
				SrcFs: filepath.Dir(src.path), SrcRemote: src.base,
				DstFs: fs + dest, DstRemote: src.base, Group: group,
			})
		}
		cancel()
		if err != nil {
			// The job is not registered yet, so any rclone job started above
			// would be invisible to ActiveJobs/PauseJob/CancelJob. Stop the
			// group rather than leave it transferring in the background.
			s.abortBackup(c, group, job.JobIDs)
			return dto.JobRef{Err: failFrom(err)}
		}
		job.JobIDs = append(job.JobIDs, jobID)
	}

	s.mu.Lock()
	s.jobs[id] = job
	s.order = append(s.order, id)
	s.mu.Unlock()
	s.journalStart(dto.Record{ID: id, Kind: job.Kind, State: "running", Title: job.Title, StartedAt: job.StartedAt.Unix(), Files: job.Files}, dto.PlanRequest{})

	first := int64(0)
	if len(job.JobIDs) > 0 {
		first = job.JobIDs[0]
	}
	return dto.JobRef{ID: id, JobID: first, Group: group}
}

// Download starts one sync/copy job per remote source. A folder's contents are
// written to localDir/<basename>; a single file lands at localDir/<basename>.
// The destination directory is created before any job starts.
func (s *Service) Download(accountID string, remotePaths []string, localDir string) dto.JobRef {
	c, fail := s.client()
	if fail != nil {
		return dto.JobRef{Err: fail}
	}
	if len(remotePaths) == 0 {
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "no file selected"}}
	}
	fs, err := accountFS(accountID)
	if err != nil {
		return dto.JobRef{Err: failFrom(err)}
	}
	dir := strings.TrimSpace(localDir)
	if dir == "" {
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "destination folder is required"}}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dto.JobRef{Err: failFrom(err)}
	}

	type source struct {
		remote string
		base   string
		isDir  bool
	}
	srcs := make([]source, 0, len(remotePaths))
	sources := make([]string, 0, len(remotePaths))
	for _, raw := range remotePaths {
		remote, err := cleanRemotePath(raw)
		if err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		st, err := statRemote(c, fs, remote)
		if err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		if st == nil {
			return dto.JobRef{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "not found"}}
		}
		srcs = append(srcs, source{remote: remote, base: remoteBase(remote), isDir: st.IsDir})
		sources = append(sources, remote)
	}

	id := fmt.Sprintf("job-%d", time.Now().UnixNano())
	group := "gui-" + id
	job := &jobState{
		ID: id, Group: group, Kind: "download", State: "running",
		StartedAt: time.Now(), DestRoot: dir, AccountID: accountID,
		Sources: sources,
	}
	if len(srcs) > 0 {
		job.Title = srcs[0].base
	}
	transfers := s.defaultTransfers()
	for _, src := range srcs {
		dst := dir
		if src.isDir {
			dst = filepath.Join(dir, src.base)
			if err := os.MkdirAll(dst, 0o755); err != nil {
				// Jobs started for earlier sources are not registered yet;
				// stop them so they cannot keep downloading invisibly.
				s.abortBackup(c, group, job.JobIDs)
				return dto.JobRef{Err: failFrom(err)}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
		var jobID int64
		var err error
		if src.isDir {
			jobID, err = c.StartSync(ctx, "sync/copy", rc.SyncIn{
				SrcFs: fs + src.remote, DstFs: dst, Group: group, Transfers: transfers,
				Retries: s.retries(),
			})
		} else {
			// A remote file path is not a directory either, so single files
			// come down with operations/copyfile.
			jobID, err = c.StartCopyFile(ctx, rc.CopyFileIn{
				SrcFs: fs + remoteDir(src.remote), SrcRemote: src.base,
				DstFs: dst, DstRemote: src.base, Group: group,
			})
		}
		cancel()
		if err != nil {
			// The job is not registered yet, so any rclone job started above
			// would be invisible to ActiveJobs/PauseJob/CancelJob. Stop the
			// group rather than leave it transferring in the background.
			s.abortBackup(c, group, job.JobIDs)
			return dto.JobRef{Err: failFrom(err)}
		}
		job.JobIDs = append(job.JobIDs, jobID)
	}

	s.mu.Lock()
	s.jobs[id] = job
	s.order = append(s.order, id)
	s.mu.Unlock()
	s.journalStart(dto.Record{ID: id, Kind: job.Kind, State: "running", Title: job.Title, StartedAt: job.StartedAt.Unix(), Files: job.Files}, dto.PlanRequest{})

	first := int64(0)
	if len(job.JobIDs) > 0 {
		first = job.JobIDs[0]
	}
	return dto.JobRef{ID: id, JobID: first, Group: group}
}

// InspectSplit reads <partsPath>/manifest.json from accountID and checks which
// parts are present. states map to the frontend as ready, incomplete (no
// manifest), corrupt (unreadable or inconsistent) and foreign (another
// format). It never hashes part contents; sizes are compared to the manifest.
func (s *Service) InspectSplit(accountID, partsPath string) dto.SplitInfo {
	var info dto.SplitInfo
	c, fail := s.client()
	if fail != nil {
		info.Err = fail
		return info
	}
	fs, err := accountFS(accountID)
	if err != nil {
		info.Err = failFrom(err)
		return info
	}
	parts, err := cleanRemotePath(partsPath)
	if err != nil {
		info.Err = failFrom(err)
		return info
	}
	info.OriginalName = splitDisplayName(parts)

	m, state, err := readRemoteManifest(c, fs, parts)
	if err != nil {
		info.Err = failFrom(err)
		return info
	}
	switch state {
	case "incomplete":
		info.State = "incomplete"
		info.Parts = countRemoteParts(c, fs, parts)
		return info
	case "foreign":
		info.State = "foreign"
		if m.Name != "" {
			info.OriginalName = m.Name
		}
		info.Size = m.Size
		info.PartCount = m.PartCount
		return info
	case "corrupt":
		info.State = "corrupt"
		if m.Name != "" {
			info.OriginalName = m.Name
		}
		info.Size = m.Size
		info.PartCount = m.PartCount
		return info
	}

	// Valid manifest: compare the listed objects with the manifest parts.
	entries, err := listRemote(c, fs, parts)
	if err != nil {
		info.Err = failFrom(err)
		return info
	}
	present := make(map[string]rc.Entry, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			present[e.Name] = e
		}
	}
	found := 0
	badSize := false
	var missing []int
	for _, p := range m.Parts {
		e, ok := present[p.Name]
		if !ok {
			missing = append(missing, p.Index)
			continue
		}
		found++
		if e.Size != p.Size {
			badSize = true
		}
	}
	info.State = "ready"
	info.Parts = found
	info.PartCount = m.PartCount
	info.OriginalName = m.Name
	if info.OriginalName == "" {
		info.OriginalName = splitDisplayName(parts)
	}
	info.Size = m.Size
	info.Missing = missing
	if len(missing) > 0 || badSize {
		info.State = "corrupt"
	}
	return info
}

// ResumeJob restarts the stored spec of a paused job in its existing group.
// This deliberately stops nothing first: the paused group's old jobs have
// already been stopped and sync/copy skips files which are already present.
func (s *Service) ResumeJob(id string) (out dto.JobRef) {
	// Validate and reserve under s.mu before any engine or RC call. A pause only
	// asks rclone to stop its jobs; the split goroutine can still be winding down
	// on the same staging directory, so starting a second one would share (and
	// delete) the first run's files. A job which is still running must never be
	// started twice either.
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "job not found"}}
	}
	if j.State == "done" {
		s.mu.Unlock()
		return dto.JobRef{Err: &dto.Fail{
			Code:    string(apperr.Unknown),
			Message: "this backup already finished; start a new one instead",
		}}
	}
	if j.State == "running" {
		s.mu.Unlock()
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.Unknown), Message: "job is still running"}}
	}
	// A Pause/Cancel landing during a resume restores the state to paused or
	// cancelled while the network work below is still in flight. The state check
	// above would then pass for a second click, so the in-flight marker has to
	// refuse it independently.
	if j.Resuming {
		s.mu.Unlock()
		return dto.JobRef{Err: &dto.Fail{
			Code:    string(apperr.Unknown),
			Message: "the job is still resuming; try again in a moment",
		}}
	}
	if j.SplitRunning {
		s.mu.Unlock()
		return dto.JobRef{Err: &dto.Fail{
			Code:    string(apperr.Unknown),
			Message: "the previous run is still stopping; try again in a moment",
		}}
	}
	// Taking ownership is the state flip itself: it happens in the same critical
	// section which validated the job, so an overlapping ResumeJob sees
	// "running" and is refused instead of starting a second set of rclone jobs
	// whose ids would replace this attempt's. PauseJob/CancelJob overwrite the
	// state while the network work below runs; the final critical sections only
	// claim the job when this reservation is still intact.
	//
	// Resuming marks the reservation as in flight. The old, already stopped ids
	// are still in JobIDs until this attempt installs its own, so to ActiveJobs
	// and the scheduler the job looks like a live run whose ids are gone. They
	// check Resuming and leave the job alone instead of recording a terminal
	// verdict which would make this resume refuse its own result.
	prevState := j.State
	// PauseJob/CancelJob bump StopSeq before their job/stopgroup call. If one
	// runs during this reservation, its stopgroup may have executed before the
	// jobs below existed, so the commit must not silently keep them running.
	stopSeq := j.StopSeq
	j.State = "running"
	j.Resuming = true
	s.mu.Unlock()

	// Any failure below releases the reservation and restores the state the job
	// had when the resume was validated, so a job which could not be restarted
	// does not look running. restoreResume only writes the state while the
	// reservation is intact, so a Pause/Cancel which landed in the meantime keeps
	// its state; the in-flight marker is cleared either way.
	defer func() {
		if out.Err != nil {
			s.restoreResume(j, prevState)
		}
	}()

	c, fail := s.client()
	if fail != nil {
		return dto.JobRef{Err: fail}
	}
	if len(j.Sources) == 0 || j.AccountID == "" {
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.Unknown), Message: "job cannot be resumed"}}
	}
	fs, err := accountFS(j.AccountID)
	if err != nil {
		return dto.JobRef{Err: failFrom(err)}
	}

	// kind selects how one plan entry is executed: a directory goes through
	// sync/copy (sync/sync for a mirror backup), a single file through copyfile
	// (sync/copy rejects a file as srcFs).
	type plan struct {
		kind      string // "sync" or "copy"
		src, dst  string
		srcRemote string
		dstRemote string
	}
	var plans []plan
	switch j.Kind {
	case "backup", "upload":
		// A backup whose large files were being uploaded as .cloudbox-part
		// sets must not fall back to a whole-file sync: that would duplicate
		// storage and never finish the split set. Rebuild the same plan with
		// the part size the job actually used and restart the split pipeline.
		if j.SplitActive {
			opts := splitOptionsFor(j.Mode, j.PartSizeMB)
			if !opts.Enabled || !j.FastFiles {
				return dto.JobRef{Err: resumelessSplitFail("its split settings are missing")}
			}
			return s.resumeSplitBackup(c, j, fs, opts, stopSeq)
		}
		for _, src := range j.Sources {
			st, err := os.Stat(src)
			if err != nil {
				return dto.JobRef{Err: failFrom(err)}
			}
			base := filepath.Base(filepath.Clean(src))
			if st.IsDir() {
				plans = append(plans, plan{
					kind: "sync", src: src, dst: fs + joinRemotePath(j.DestRoot, base),
				})
				continue
			}
			plans = append(plans, plan{
				kind: "copy", src: filepath.Dir(filepath.Clean(src)), srcRemote: base,
				dst: fs + j.DestRoot, dstRemote: base,
			})
		}
	case "copy":
		// CopyCloud does not store the source account, so there is no safe way
		// to rebuild the request. Say so instead of the generic refusal.
		return dto.JobRef{Err: &dto.Fail{
			Code:    string(apperr.ProviderUnsupported),
			Message: "this copy cannot be resumed",
			Detail:  "Cloud-to-cloud copies are not saved with a resume plan. Start the copy again from the Files page.",
		}}
	case "download":
		if strings.TrimSpace(j.DestRoot) == "" {
			return dto.JobRef{Err: &dto.Fail{Code: string(apperr.Unknown), Message: "job cannot be resumed"}}
		}
		if err := os.MkdirAll(j.DestRoot, 0o755); err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		for _, raw := range j.Sources {
			remote, err := cleanRemotePath(raw)
			if err != nil {
				return dto.JobRef{Err: failFrom(err)}
			}
			st, err := statRemote(c, fs, remote)
			if err != nil {
				return dto.JobRef{Err: failFrom(err)}
			}
			if st == nil {
				return dto.JobRef{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "not found"}}
			}
			if st.IsDir {
				dst := filepath.Join(j.DestRoot, remoteBase(remote))
				if err := os.MkdirAll(dst, 0o755); err != nil {
					return dto.JobRef{Err: failFrom(err)}
				}
				plans = append(plans, plan{kind: "sync", src: fs + remote, dst: dst})
				continue
			}
			plans = append(plans, plan{
				kind: "copy", src: fs + remoteDir(remote), srcRemote: remoteBase(remote),
				dst: j.DestRoot, dstRemote: remoteBase(remote),
			})
		}
	default:
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.Unknown), Message: "job cannot be resumed"}}
	}
	if len(plans) == 0 {
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.Unknown), Message: "job cannot be resumed"}}
	}

	// With no split pipeline to rebuild, resume the same per-source plan the
	// original backup used. Mirror jobs must keep sync/sync so the resumed run
	// does not silently turn into a copy, and the stored exclude patterns must
	// travel with every sync/copy exactly like StartBackup passes them.
	syncKind := "sync/copy"
	if j.Mode == "mirror" {
		syncKind = "sync/sync"
	}
	transfers := s.defaultTransfers()
	newIDs := make([]int64, 0, len(plans))
	for _, p := range plans {
		ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
		var jobID int64
		var err error
		if p.kind == "copy" {
			jobID, err = c.StartCopyFile(ctx, rc.CopyFileIn{
				SrcFs: p.src, SrcRemote: p.srcRemote,
				DstFs: p.dst, DstRemote: p.dstRemote, Group: j.Group,
			})
		} else {
			jobID, err = c.StartSync(ctx, syncKind, rc.SyncIn{
				SrcFs: p.src, DstFs: p.dst, Group: j.Group, Transfers: transfers,
				Exclude: planner.RcloneRules(j.Excludes), Retries: s.retries(),
			})
		}
		cancel()
		if err != nil {
			// The jobs started above are not tracked yet; stop the group so
			// they cannot keep transferring invisibly (mirrors StartBackup).
			s.abortBackup(c, j.Group, newIDs)
			return dto.JobRef{Err: failFrom(err)}
		}
		newIDs = append(newIDs, jobID)
	}

	s.mu.Lock()
	// The network work above released the lock. Only claim the job when the
	// reservation taken at entry is still intact: PauseJob/CancelJob write
	// "paused"/"cancelled" while the jobs start, and must not be overwritten
	// back into a running state which would resurrect a stopped transfer. A
	// changed state means the user stopped the job, so stop what this attempt
	// started and refuse. A changed StopSeq catches the window where the stop's
	// stopgroup ran before these jobs were started but the state write has not
	// landed yet.
	if j.State != "running" || j.SplitRunning || j.StopSeq != stopSeq {
		msg := "the job was paused or cancelled while it was resuming"
		if j.SplitRunning {
			msg = "the previous run is still stopping; try again in a moment"
		}
		s.mu.Unlock()
		s.abortBackup(c, j.Group, newIDs)
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.Unknown), Message: msg}}
	}
	// Replace the old, stopped job ids: ActiveJobs waits for every id in the
	// list, and a stopped id would otherwise keep the resumed job paused or
	// mark it failed. Installing the ids and clearing the in-flight marker
	// happen in one critical section, so callers can never observe "running"
	// with the old ids plus no marker.
	j.JobIDs = newIDs
	j.State = "running"
	j.Resuming = false
	j.StartedAt = time.Now()
	j.TotalBytes = 0
	j.Bytes = 0
	j.Speed = 0
	j.ETA = nil
	j.Current = ""
	j.Failed = 0
	s.mu.Unlock()

	first := newIDs[0]
	return dto.JobRef{ID: j.ID, JobID: first, Group: j.Group}
}

// resumelessSplitFail is the user-facing refusal for a split backup whose
// stored plan cannot be rebuilt. Silently syncing the large files whole would
// duplicate storage and leave the .cloudbox-part set incomplete, so starting a
// new backup is the only safe option.
func resumelessSplitFail(why string) *dto.Fail {
	return &dto.Fail{
		Code:    string(apperr.Unknown),
		Message: "this split backup cannot be resumed",
		Detail:  "The large-file split plan could not be rebuilt because " + why + ". Start a new backup instead.",
	}
}

// resumeSplitBackup restarts a paused or failed split backup in its existing
// group. The plan is rebuilt from the stored sources with the part size the
// job actually started with, the small files travel in the same filtered
// sync/copy that StartBackup uses, and runSplitBackup then completes the parts
// directories. An empty plan is refused: the job was split, so falling back to
// a whole-file sync would duplicate storage and never finish the split set.
func (s *Service) resumeSplitBackup(c *rc.Client, j *jobState, fs string, opts split.Options, stopSeq int) dto.JobRef {
	plan, err := buildSplitPlan(j.Sources, j.DestRoot, opts, j.Excludes)
	if err != nil {
		return dto.JobRef{Err: resumelessSplitFail("the sources could not be read (" + err.Error() + ")")}
	}
	if len(plan.files) == 0 {
		return dto.JobRef{Err: resumelessSplitFail("its large files are no longer available")}
	}

	// Mirror StartBackup: a folder source with large files gets one filtered
	// small-file sync; a source without large files resumes as a normal sync
	// or copyfile. buildSplitPlan has already stat'ed every source, so a
	// missing source cannot leave half-started jobs behind.
	kind := "sync/copy"
	if j.Mode == "mirror" {
		kind = "sync/sync"
	}
	transfers := s.defaultTransfers()
	newIDs := make([]int64, 0, len(j.Sources)+1)
	for i, src := range j.Sources {
		sp := plan.sources[i]
		base := filepath.Base(filepath.Clean(src))
		if len(sp.large) > 0 {
			if !sp.copySmall {
				continue
			}
			dstFs := fs + joinRemotePath(j.DestRoot, base)
			ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
			smallJobID, err := startSmallFileSync(ctx, c, src, dstFs, j.Group, transfers, opts.Threshold, j.Excludes)
			cancel()
			if err != nil {
				s.abortBackup(c, j.Group, newIDs)
				return dto.JobRef{Err: failFrom(err)}
			}
			newIDs = append(newIDs, smallJobID)
			continue
		}

		if !sp.isDir {
			ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
			copyJobID, err := c.StartCopyFile(ctx, rc.CopyFileIn{
				SrcFs:     filepath.Dir(filepath.Clean(src)),
				SrcRemote: base,
				DstFs:     fs + j.DestRoot,
				DstRemote: base,
				Group:     j.Group,
			})
			cancel()
			if err != nil {
				s.abortBackup(c, j.Group, newIDs)
				return dto.JobRef{Err: failFrom(err)}
			}
			newIDs = append(newIDs, copyJobID)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
		syncJobID, err := c.StartSync(ctx, kind, rc.SyncIn{
			SrcFs: src, DstFs: fs + joinRemotePath(j.DestRoot, base), Group: j.Group,
			Transfers: transfers, Exclude: planner.RcloneRules(j.Excludes), Retries: s.retries(),
		})
		cancel()
		if err != nil {
			s.abortBackup(c, j.Group, newIDs)
			return dto.JobRef{Err: failFrom(err)}
		}
		newIDs = append(newIDs, syncJobID)
	}

	s.mu.Lock()
	// The network work above (rebuilding the plan and starting the small-file
	// jobs) released the lock. ResumeJob reserved the job by flipping it to
	// "running" in the same critical section which validated it, and that is the
	// only way this attempt can still see "running" here: a second resume is
	// refused at entry. PauseJob/CancelJob overwrite the state with "paused" or
	// "cancelled" during the window, so any other value means the user stopped
	// the job. StopSeq catches the same stop before its state write lands: its
	// stopgroup may already have run, so these ids could otherwise keep running.
	// Stop the jobs this attempt started and refuse instead of resurrecting a
	// cancelled backup.
	if j.State != "running" || j.SplitRunning || j.StopSeq != stopSeq {
		msg := "the job was paused or cancelled while it was resuming"
		if j.SplitRunning {
			msg = "the previous run is still stopping; try again in a moment"
		}
		s.mu.Unlock()
		s.abortBackup(c, j.Group, newIDs)
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.Unknown), Message: msg}}
	}
	// Replace the old, stopped ids: ActiveJobs waits for every id, and a
	// stopped id would keep the resumed job paused or mark it failed. The
	// pending marker keeps it from completing while parts are hashed/staged.
	// Clearing Resuming in the same critical section which installs the new
	// ids means ActiveJobs never sees "running" with the old ids and no
	// in-flight marker.
	j.JobIDs = append(newIDs, pendingSplitJobID)
	j.State = "running"
	j.Resuming = false
	j.Phase = "hashing"
	j.StartedAt = time.Now()
	j.TotalBytes = plan.totalBytes()
	j.Bytes = 0
	j.Speed = 0
	j.ETA = nil
	j.Current = ""
	j.Failed = 0
	// ResumeJob already took the reservation at entry; this critical section
	// verified it is still ours. Setting SplitRunning here means a concurrent
	// ResumeJob cannot see a terminal-looking job and launch a second goroutine
	// between the state flip and the pipeline start.
	j.SplitRunning = true
	s.mu.Unlock()

	// runSplitBackup reads the concurrency and retry settings from s.store, so
	// it is only ever started for a live Service.
	go s.runSplitBackup(c, j, plan)

	first := int64(0)
	for _, id := range newIDs {
		if id > 0 {
			first = id
			break
		}
	}
	return dto.JobRef{ID: j.ID, JobID: first, Group: j.Group}
}

// restoreResume releases the ownership reservation ResumeJob took when the job
// was validated. It restores the pre-resume state only while the reservation is
// still in place: PauseJob/CancelJob write "paused"/"cancelled" during the
// network work, and that newer state must win. The in-flight marker is always
// cleared, because the resume attempt is over whether or not it succeeded.
func (s *Service) restoreResume(j *jobState, prev string) {
	s.mu.Lock()
	if j.State == "running" {
		j.State = prev
	}
	j.Resuming = false
	s.mu.Unlock()
}

// ---------------------------------------------------------------- helpers

func (s *Service) defaultTransfers() int {
	transfers := s.store.Get().Backup.Transfers
	if transfers <= 0 {
		transfers = opsDefaultTransfers
	}
	return transfers
}

// retries maps the auto-retry switch to rclone's Retries option: off means a
// failed file is reported straight away instead of being retried.
func (s *Service) retries() int {
	if s.store.Get().Backup.AutoRetry {
		return 3
	}
	return 1
}

// accountFS validates a remote name and returns its rclone prefix. Rejecting
// ":" guarantees the result names exactly one remote.
func accountFS(accountID string) (string, error) {
	id := strings.TrimSpace(accountID)
	if id == "" {
		return "", errors.New("account is required")
	}
	if strings.Contains(id, ":") {
		return "", errors.New("invalid account")
	}
	return id + ":", nil
}

// cleanRemotePath normalises a path relative to a remote root and rejects
// traversal. It never returns an empty path.
func cleanRemotePath(raw string) (string, error) {
	p, err := cleanRemotePathAllowEmpty(raw)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errors.New("path is required")
	}
	return p, nil
}

// cleanRemotePathAllowEmpty is cleanRemotePath but permits the account root.
func cleanRemotePathAllowEmpty(raw string) (string, error) {
	p := strings.Trim(strings.TrimSpace(raw), "/")
	if strings.Contains(p, ":") {
		return "", errors.New("invalid remote path")
	}
	segments := strings.Split(p, "/")
	clean := make([]string, 0, len(segments))
	for _, seg := range segments {
		switch seg {
		case "", ".":
			continue
		case "..":
			return "", errors.New("invalid remote path")
		}
		clean = append(clean, seg)
	}
	return strings.Join(clean, "/"), nil
}

// joinRemotePath joins two relative remote path components.
func joinRemotePath(dest, base string) string {
	return strings.Trim(dest+"/"+base, "/")
}

func remoteBase(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// remoteDir returns the parent of a slash-separated remote path, or "" for a
// path at the account root.
func remoteDir(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return strings.Trim(p[:i], "/")
	}
	return ""
}

// splitDisplayName derives a user-facing name from a .cloudbox-part folder.
func splitDisplayName(parts string) string {
	base := remoteBase(parts)
	return strings.TrimSuffix(base, ".cloudbox-part")
}

func isSplitDirPath(p string) bool {
	return strings.HasSuffix(strings.TrimRight(p, "/"), ".cloudbox-part")
}

// statRemote probes one object. A nil entry with a nil error means it does not
// exist. Each call has its own timeout.
func statRemote(c *rc.Client, fs, remote string) (*rc.Entry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
	defer cancel()
	return c.Stat(ctx, fs, remote, rc.ListOpt{})
}

// listRemote lists one remote directory with its own timeout.
func listRemote(c *rc.Client, fs, remote string) ([]rc.Entry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.List(ctx, fs, remote, rc.ListOpt{})
}

// purgeRemote removes a directory tree. A purge of something already gone is
// treated as success so DeleteCloud counts it as deleted.
func purgeRemote(c *rc.Client, fs, remote string) error {
	ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
	defer cancel()
	err := c.Purge(ctx, fs, remote)
	if err != nil && rc.IsNotFound(err) {
		return nil
	}
	return err
}

// readRemoteManifest returns the manifest for a split directory. operations/cat
// is tried first; if it is unavailable (or the body is not JSON) the manifest
// is copied to a temporary directory and read with split.ReadManifestFile. A
// nil error and the state "incomplete" means the manifest does not exist.
func readRemoteManifest(c *rc.Client, fs, parts string) (split.Manifest, string, error) {
	remote := parts + "/" + manifestName

	catCtx, catCancel := context.WithTimeout(context.Background(), opsTimeout)
	var raw json.RawMessage
	catErr := c.Call(catCtx, "operations/cat", map[string]any{
		"fs":     fs,
		"remote": remote,
		// A valid manifest is far smaller than this; cap an accidental read.
		"count": maxManifestBytes + 1,
	}, &raw)
	catCancel()
	if catErr == nil && len(raw) > 0 {
		if len(raw) > maxManifestBytes {
			return split.Manifest{}, "corrupt", nil
		}
		m, state := classifyManifest(raw)
		return m, state, nil
	}
	// rclone 1.76 answers an unknown method with the same 404 it uses for a
	// missing object, so the method-absent case must not be read as "no
	// manifest". An object 404 is the normal "incomplete" signal.
	if catErr != nil && !rc.IsMethodMissing(catErr) && rc.IsNotFound(catErr) {
		return split.Manifest{}, "incomplete", nil
	}

	// Prove the manifest exists before copying it. rclone's copyfile on a
	// missing Drive object retries until the call times out (tens of seconds)
	// instead of answering 404, which used to make opening a not-yet-complete
	// split look like a hung restore. operations/stat answers 404 immediately.
	if _, statErr := statRemote(c, fs, remote); statErr != nil {
		if rc.IsNotFound(statErr) {
			return split.Manifest{}, "incomplete", nil
		}
		return split.Manifest{}, "", statErr
	}

	tmp, err := os.MkdirTemp("", "cloudnest-split-*")
	if err != nil {
		return split.Manifest{}, "", err
	}
	defer os.RemoveAll(tmp)

	cpCtx, cpCancel := context.WithTimeout(context.Background(), opsTimeout)
	cpErr := c.CopyFile(cpCtx, rc.CopyFileIn{
		SrcFs:     fs,
		SrcRemote: remote,
		DstFs:     tmp,
		DstRemote: manifestName,
	})
	cpCancel()
	if cpErr != nil {
		// The stat above proved the manifest exists, so a 404 here means it was
		// removed in between: still the normal "incomplete" signal.
		if rc.IsNotFound(cpErr) {
			return split.Manifest{}, "incomplete", nil
		}
		return split.Manifest{}, "", cpErr
	}

	if m, readErr := split.ReadManifestFile(tmp); readErr == nil {
		return m, "", nil
	}
	data, readErr := os.ReadFile(filepath.Join(tmp, manifestName))
	if readErr != nil {
		return split.Manifest{}, "", readErr
	}
	m, state := classifyManifest(data)
	if state == "" {
		state = "corrupt"
	}
	return m, state, nil
}

// classifyManifest strictly decodes and validates manifest bytes. It returns
// an empty state for a valid manifest, "foreign" for another format and
// "corrupt" for invalid JSON or an inconsistent structure.
func classifyManifest(data []byte) (split.Manifest, string) {
	// Probe the format first: another format may legitimately carry fields
	// this package does not know, so strict decoding must not turn it into
	// "corrupt".
	var probe split.Manifest
	if err := json.Unmarshal(data, &probe); err != nil {
		return split.Manifest{}, "corrupt"
	}
	if probe.Format != split.Format {
		return probe, "foreign"
	}

	// For our own format, decode exactly like split.ReadManifestFile.
	var m split.Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return split.Manifest{}, "corrupt"
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return split.Manifest{}, "corrupt"
	}
	if err := split.ValidateManifest(m); err != nil {
		return m, "corrupt"
	}
	return m, ""
}

// countRemoteParts counts part-XXXX.bin objects in a directory which has no
// manifest. Transport failures yield zero, keeping the state "incomplete".
func countRemoteParts(c *rc.Client, fs, parts string) int {
	entries, err := listRemote(c, fs, parts)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir && opsPartNameRE.MatchString(e.Name) {
			n++
		}
	}
	return n
}
