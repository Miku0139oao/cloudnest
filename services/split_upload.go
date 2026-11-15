// Split upload support for StartBackup.
//
// The remote layout and the local staging primitives live in internal/split.
// This file glues them to rclone's RC API: it walks the sources, hashes and
// stages large files, uploads each parts directory, verifies the remote sizes,
// retries individual parts and finally uploads manifest.json.
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloudnest/internal/planner"
	"cloudnest/internal/platform"
	"cloudnest/internal/rc"
	"cloudnest/internal/split"
)

const (
	// splitMaxUploadWait bounds one parts-directory upload. Individual HTTP
	// calls below still use their own, much shorter timeouts.
	splitMaxUploadWait = 2 * time.Hour
	// splitPollInterval is the job/status poll cadence while waiting.
	splitPollInterval = 500 * time.Millisecond
	// splitVerifyRetries is the number of extra copyfile rounds after the
	// initial size check. Each round re-lists the remote directory.
	splitVerifyRetries = 2
	// splitRCCallTimeout bounds one RC control call.
	splitRCCallTimeout = 30 * time.Second
	// splitListTimeout is more generous because listing can be slow on large
	// cloud directories.
	splitListTimeout = 60 * time.Second
	// splitPartCopyTimeout bounds one corrective operations/copyfile call.
	splitPartCopyTimeout = 10 * time.Minute
	// splitManifestTimeout bounds the final operations/copyfile of manifest.json.
	splitManifestTimeout = 2 * time.Minute
	// splitSideJobWait bounds how long the split run waits for the small-file
	// sync/copy after its parts are done. Unlike one part (2 hours), a folder
	// full of small files can legitimately take many hours.
	splitSideJobWait = 24 * time.Hour
	// splitSideWaitPoll is the side-job poll cadence. Polling also keeps rclone
	// from expiring the finished job before the result is read.
	splitSideWaitPoll = time.Second
	// splitStatusRetries is how many consecutive job/status errors are
	// tolerated before the side job is declared failed.
	splitStatusRetries = 5

	// pendingSplitJobID is a reserved job id (never sent to rclone) which keeps
	// ActiveJobs from declaring the backup finished while the split goroutine is
	// hashing or staging. rc.Client.JobStatus rejects non-positive ids locally,
	// so ActiveJobs treats the job as unfinished until the goroutine sets the
	// final state. This is needed because the split work has long periods with
	// no real RC job to report, and ActiveJobs' current contract is that an
	// empty JobIDs list means "finished".
	pendingSplitJobID int64 = -1
)

var errSplitStopped = errors.New("split upload stopped")
var errSplitSourceChanged = errors.New("file changed during backup")

// splitFile describes one large local file queued for split upload.
type splitFile struct {
	path     string    // absolute local path
	rel      string    // slash-separated path relative to a folder source
	name     string    // base file name
	size     int64     // bytes
	modTime  time.Time // observed while planning
	destDir  string    // remote directory relative to the account root
	partsDir string    // <filename>.cloudbox-part
}

// splitSourcePlan is the per-source outcome of the large-file walk.
type splitSourcePlan struct {
	isDir     bool
	copySmall bool // the source folder still needs a small-file sync/copy
	large     []splitFile
}

// splitBackupPlan is the complete classification for one StartBackup call.
type splitBackupPlan struct {
	sources []splitSourcePlan
	files   []splitFile // flattened, source order
	// opts is the configuration the plan was built with. The split goroutine
	// needs it again to lay out each file's parts with the part size the user
	// picked in Settings.
	opts split.Options
}

func (p *splitBackupPlan) totalBytes() int64 {
	var n int64
	for _, f := range p.files {
		n += f.size
	}
	return n
}

// splitMaxParts is the product's ceiling for one split set. The manifest format
// allows up to 1000 parts, but manifest.json itself is capped at 64 KiB and a
// part entry costs roughly 130 bytes, so 256 keeps a wide safety margin.
// Within this ceiling the part size picked in Settings is honoured as-is.
const splitMaxParts = 256

// splitOptionsFor returns the split options for a backup mode, applying the
// part size the user picked in Settings (in MiB; <= 0 keeps the default).
// Mirror mode is deliberately excluded: sync/sync would delete the remote
// .cloudbox-part directories because they do not exist in the local source tree.
func splitOptionsFor(mode string, partSizeMB int) split.Options {
	opts := split.DefaultOptions()
	opts.MaxParts = splitMaxParts
	if mode == "mirror" {
		opts.Enabled = false
	}
	if partSizeMB > 0 {
		opts.PartSize = int64(partSizeMB) << 20
	}
	return opts
}

// splitStagingSpace returns the free space required before staging a file:
// min(size, 4 GiB) * 1.2 + 256 MiB. The 1.2 factor is rounded up.
func splitStagingSpace(size int64) int64 {
	const (
		fourGiB   int64 = 4 << 30
		overhead  int64 = 256 << 20
		numerator       = 6 // 1.2 == 6/5
	)
	if size < 0 {
		size = 0
	}
	capped := size
	if capped > fourGiB {
		capped = fourGiB
	}
	return (capped*numerator+4)/5 + overhead
}

// splitJobStagingDir returns <StagingDir>/split-<jobID>.
func splitJobStagingDir(root, jobID string) string {
	return filepath.Join(root, "split-"+jobID)
}

// splitFileStagingDir returns the staging directory for the nth large file of
// a job: <StagingDir>/split-<jobID>/<n>.
func splitFileStagingDir(root, jobID string, n int) string {
	return filepath.Join(splitJobStagingDir(root, jobID), strconv.Itoa(n))
}

// buildSplitPlan stats every source and walks folders to find files at or
// above the split threshold. Sources without large files are left to the
// caller's normal sync/copy path. Excluded paths are skipped so they are never
// split; the small-file sync filters them out on rclone's side.
func buildSplitPlan(sources []string, dest string, opts split.Options, excludes []string) (*splitBackupPlan, error) {
	plan := &splitBackupPlan{sources: make([]splitSourcePlan, len(sources)), opts: opts}
	for i, src := range sources {
		st, err := os.Stat(src)
		if err != nil {
			return nil, err
		}
		sp := splitSourcePlan{isDir: st.IsDir()}
		if !st.IsDir() {
			if split.ShouldSplit(st.Size(), opts) {
				f, err := newSplitFile(src, filepath.Base(filepath.Clean(src)), "", st.Size(), st.ModTime(), dest)
				if err != nil {
					return nil, err
				}
				sp.large = append(sp.large, f)
				plan.files = append(plan.files, f)
			}
			plan.sources[i] = sp
			continue
		}

		if !opts.Enabled {
			plan.sources[i] = sp
			continue
		}

		base := filepath.Base(filepath.Clean(src))
		rootDest := joinRemotePath(dest, base)
		err = filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil || !info.Mode().IsRegular() || !split.ShouldSplit(info.Size(), opts) {
				return nil
			}
			rel, relErr := filepath.Rel(src, p)
			if relErr != nil {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			if planner.Excluded(relSlash, excludes) {
				return nil
			}
			destDir := rootDest
			if dir := path.Dir(relSlash); dir != "." && dir != "" {
				destDir = joinRemotePath(rootDest, dir)
			}
			f, fileErr := newSplitFile(p, info.Name(), relSlash, info.Size(), info.ModTime(), destDir)
			if fileErr != nil {
				return fileErr
			}
			sp.large = append(sp.large, f)
			return nil
		})
		if err != nil {
			return nil, err
		}
		sp.copySmall = len(sp.large) > 0
		plan.files = append(plan.files, sp.large...)
		plan.sources[i] = sp
	}
	return plan, nil
}

// newSplitFile validates the remote parts-directory name for one file.
func newSplitFile(localPath, name, rel string, size int64, modTime time.Time, destDir string) (splitFile, error) {
	parts := split.PartsDirName(name)
	if parts == "" {
		return splitFile{}, fmt.Errorf("cannot split file with an unsupported name: %s", localPath)
	}
	return splitFile{
		path:     localPath,
		rel:      rel,
		name:     name,
		size:     size,
		modTime:  modTime,
		destDir:  destDir,
		partsDir: parts,
	}, nil
}

// filteredSyncRequest builds a sync/copy request which only transfers files
// smaller than maxSize and outside excludes. rc.SyncIn only knows a single
// Exclude list, so this helper speaks the RC shape directly to combine the
// size cap with the user's patterns.
func filteredSyncRequest(srcFs, dstFs, group string, transfers int, maxSize int64, excludes []string) map[string]any {
	cfg := map[string]any{"DryRun": false}
	if transfers > 0 {
		cfg["Transfers"] = transfers
	}
	if maxSize < 0 {
		maxSize = 0
	}
	// rclone parses a suffixless SizeSuffix as KiB; the cap is a byte count.
	filter := map[string]any{"MaxSize": strconv.FormatInt(maxSize, 10) + "B"}
	if len(excludes) > 0 {
		// rclone decodes _filter by Go field name: ExcludeRule is the exclude
		// rules option ("exclude"). "Exclude" is silently ignored and would
		// let excluded files through.
		filter["ExcludeRule"] = planner.RcloneRules(excludes)
		filter["IgnoreCase"] = true
	}
	return map[string]any{
		"srcFs":   srcFs,
		"dstFs":   dstFs,
		"_async":  true,
		"_group":  group,
		"_config": cfg,
		"_filter": filter,
	}
}

// startSmallFileSync starts the one sync/copy per folder source which carries
// everything below the split threshold. Large files are uploaded separately.
func startSmallFileSync(ctx context.Context, c *rc.Client, srcFs, dstFs, group string, transfers int, threshold int64, excludes []string) (int64, error) {
	req := filteredSyncRequest(srcFs, dstFs, group, transfers, threshold-1, excludes)
	var resp struct {
		JobID int64 `json:"jobid"`
	}
	if err := c.Call(ctx, "sync/copy", req, &resp); err != nil {
		return 0, err
	}
	if resp.JobID <= 0 {
		return 0, errors.New("rclone did not return a job id")
	}
	return resp.JobID, nil
}

// runSplitBackup processes every large file sequentially. The parent job is
// updated under s.mu and finalized exactly once.
func (s *Service) runSplitBackup(c *rc.Client, j *jobState, plan *splitBackupPlan) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartBackup marks the job as split-running so the scheduler and ActiveJobs
	// do not treat the gaps between hashing/staging/uploading as finished. The
	// flag must be cleared on every return path (done, failed, paused,
	// cancelled); a leaked true value would block scheduled backups forever.
	defer func() {
		s.mu.Lock()
		j.SplitRunning = false
		s.mu.Unlock()
	}()

	// The small-file sync/copy shares the group with the parts. Poll it from
	// the start: rclone forgets finished jobs after about a minute, so asking
	// for its result only after hours of part uploads would lose it.
	sideJobs, sideResults := s.watchSplitSideJobs(ctx, c, j)

	// Any non-success exit stops the small-file job too. Leaving it running
	// would make Retry/Resume start a second sync over the same tree, and the
	// user would have no way to stop the orphan.
	completed := false
	defer func() {
		if !completed {
			s.stopSplitSideJobs(c, j)
		}
	}()

	stagingRoot, err := platform.StagingDir()
	if err != nil {
		s.failSplitJob(j, "cannot prepare staging area: "+err.Error())
		return
	}
	jobStaging := splitJobStagingDir(stagingRoot, j.ID)
	if err := os.MkdirAll(jobStaging, 0o755); err != nil {
		s.failSplitJob(j, "cannot prepare staging area: "+err.Error())
		return
	}
	defer os.RemoveAll(jobStaging)

	var done int64
	// workers is the user's "files at once" setting: it caps how many parts of
	// one large file may be in flight together, instead of the fixed 4 the
	// split upload used to force.
	workers := s.defaultTransfers()
	// attempts mirrors the auto-retry setting the old sync/copy job carried.
	attempts := s.retries()
	for i, f := range plan.files {
		if s.splitStopped(j) {
			// CancelJob/PauseJob owns the state and has already recorded it.
			return
		}
		err := s.uploadSplitFile(ctx, cancel, c, j, jobStaging, i+1, f, plan.opts, workers, attempts, done)
		if err == nil {
			done += f.size
			continue
		}
		if errors.Is(err, errSplitStopped) {
			return
		}
		s.failSplitJob(j, err.Error())
		return
	}

	// Only after the small files are in as well may the backup be reported as
	// complete: otherwise closing the window would kill their upload silently.
	if err := s.awaitSplitSideJobs(j, sideJobs, sideResults); err != nil {
		if errors.Is(err, errSplitStopped) {
			return
		}
		s.failSplitJob(j, err.Error())
		return
	}

	// A pause or cancel that arrived during the last verify/manifest step owns
	// the state; never overwrite it with "done".
	if s.splitStopped(j) {
		return
	}

	completed = true
	s.mu.Lock()
	j.State = "done"
	j.Phase = "done"
	j.Bytes = done
	j.Speed = 0
	j.ETA = nil
	j.Current = ""
	s.mu.Unlock()
	s.finishJob(j)
}

// splitSideJobIDs returns the backup's other rclone job ids (the filtered
// small-file sync/copy or single-file copy started by StartBackup/ResumeJob).
// pendingSplitJobID is a local marker, not an rclone job.
func (s *Service) splitSideJobIDs(j *jobState) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int64, 0, len(j.JobIDs))
	for _, id := range j.JobIDs {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// watchSplitSideJobs starts one poller per small-file job and returns how many
// results awaitSplitSideJobs should collect.
func (s *Service) watchSplitSideJobs(ctx context.Context, c *rc.Client, j *jobState) (int, <-chan error) {
	ids := s.splitSideJobIDs(j)
	results := make(chan error, len(ids))
	for _, id := range ids {
		go func(id int64) {
			results <- s.waitSideJob(ctx, c, j, id)
		}(id)
	}
	return len(ids), results
}

// stopSplitSideJobs stops the backup's small-file jobs. It is called on every
// non-success exit so a failed or paused run cannot leave a sync/copy running
// behind the user's back.
func (s *Service) stopSplitSideJobs(c *rc.Client, j *jobState) {
	if c == nil || len(s.splitSideJobIDs(j)) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), splitRCCallTimeout)
	defer cancel()
	_ = c.JobStopGroup(ctx, j.Group)
}

// waitSideJob polls one small-file job until it finishes. It differs from
// waitSplitJob on purpose: a folder of small files may legitimately take many
// hours (this budget is 24h, not the 2h cap for one part), and transient
// job/status errors are retried a few times instead of failing a healthy
// backup on one hiccup.
func (s *Service) waitSideJob(ctx context.Context, c *rc.Client, j *jobState, jobID int64) error {
	deadline := time.Now().Add(splitSideJobWait)
	failures := 0
	for {
		if err := s.splitStopError(j); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("the small-file transfer did not finish within 24 hours")
		}

		callCtx, callCancel := context.WithTimeout(context.Background(), splitRCCallTimeout)
		status, err := c.JobStatus(callCtx, jobID)
		callCancel()
		if err != nil {
			if stopErr := s.splitStopError(j); stopErr != nil {
				return stopErr
			}
			failures++
			if failures > splitStatusRetries {
				return fmt.Errorf("checking the small-file transfer: %w", err)
			}
		} else {
			failures = 0
			if status.Finished {
				if status.Success {
					return nil
				}
				if stopErr := s.splitStopError(j); stopErr != nil {
					return stopErr
				}
				if status.Error != "" {
					return errors.New(status.Error)
				}
				return errors.New("the small-file transfer failed")
			}
		}

		timer := time.NewTimer(splitSideWaitPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errSplitStopped
		case <-timer.C:
		}
	}
}

// awaitSplitSideJobs collects the background results of the small-file jobs.
// A failure there must fail the whole backup instead of being recorded as a
// successful one.
func (s *Service) awaitSplitSideJobs(j *jobState, count int, results <-chan error) error {
	var firstErr error
	for i := 0; i < count; i++ {
		err := <-results
		if err == nil || firstErr != nil {
			continue
		}
		if errors.Is(err, errSplitStopped) || errors.Is(err, context.Canceled) {
			firstErr = errSplitStopped
			continue
		}
		if stopErr := s.splitStopError(j); stopErr != nil {
			firstErr = errSplitStopped
			continue
		}
		firstErr = fmt.Errorf("waiting for the small-file transfer: %w", err)
	}
	return firstErr
}

// uploadSplitFile runs hash -> stage/upload -> verify -> manifest for one
// file. Parts are staged and uploaded through a bounded pipeline, so several
// parts are in flight at once. The file's staging directory is always removed
// before returning.
func (s *Service) uploadSplitFile(ctx context.Context, cancel context.CancelFunc, c *rc.Client, j *jobState, jobStaging string, seq int, f splitFile, opts split.Options, workers, attempts int, done int64) error {
	dir := filepath.Join(jobStaging, strconv.Itoa(seq))
	defer os.RemoveAll(dir)

	// The part layout can be computed without reading the file, so a resumed
	// backup can skip a set that a previous run already completed instead of
	// hashing and re-uploading the whole file (and deleting its manifest).
	// Stat first: a file modified after the plan was built must be rebuilt,
	// never skipped on the plan's stale size/mtime.
	m := splitManifest(f, opts)
	remoteParts := joinRemotePath(f.destDir, f.partsDir)
	if err := s.checkSplitSource(f); err != nil {
		return err
	}
	if s.remoteSplitComplete(ctx, c, j, f, m, remoteParts) {
		if s.log != nil {
			s.log.Info("service: split set already complete", "file", f.name, "parts", m.PartCount, "path", remoteParts)
		}
		return nil
	}

	free, err := platform.FreeBytes(jobStaging)
	if err != nil {
		return fmt.Errorf("cannot check free disk space: %w", err)
	}
	need := splitStagingSpace(f.size)
	if free < uint64(need) {
		return fmt.Errorf("not enough free disk space for %s: need about %d MB, have %d MB",
			f.name, (need+(1<<20)-1)>>20, free>>20)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create staging directory: %w", err)
	}

	// ------------------------------------------------------------- hashing
	s.setSplitProgress(j, "hashing", done)
	if err := split.Hash(ctx, f.path, &m, s.splitProgress(j, done, cancel)); err != nil {
		return s.splitFileError(j, "hashing "+f.name, err)
	}
	if opts.PartSize > 0 && m.ChunkSize > opts.PartSize && s.log != nil {
		// The configured part size is honoured up to splitMaxParts; beyond
		// that the chunk grows so the manifest stays within its size limit.
		s.log.Info("service: split part size increased to stay within the part limit",
			"file", f.name, "requested", opts.PartSize, "effective", m.ChunkSize, "parts", m.PartCount)
	}
	if err := split.ValidateManifest(m); err != nil {
		return fmt.Errorf("invalid manifest for %s: %w", f.name, err)
	}
	if m.Size != f.size {
		return errSplitSourceChanged
	}
	if err := s.checkSplitSource(f); err != nil {
		return err
	}

	// -------------------------------------------------- staging + uploading
	// Parts are staged one at a time and uploaded as soon as each one is on
	// disk, with up to `workers` copyfile jobs running at once. This replaces
	// the old "stage the whole file, then one sync/copy with a hard-coded cap
	// of 4 transfers" step: several parts are genuinely in flight together and
	// the user's transfer setting is honoured.
	s.setSplitProgress(j, "uploading", done)

	// A manifest is rclone's completion marker. Remove any stale one before
	// touching the parts directory so a failed run cannot leave an old marker
	// claiming the new set is complete. A missing object is expected.
	if err := deleteRemoteManifest(c, j.AccountID, remoteParts); err != nil {
		if s.log != nil {
			s.log.Warn("service: could not clear previous split manifest", "path", remoteParts, "err", err)
		}
	}

	if err := s.uploadSplitParts(ctx, c, j, f, dir, remoteParts, m, workers, attempts); err != nil {
		return err
	}
	if err := s.checkSplitSource(f); err != nil {
		return err
	}

	// ------------------------------------------------------------- verifying
	if err := s.verifySplitParts(ctx, c, j, dir, remoteParts, m); err != nil {
		return err
	}
	// verifySplitParts can spend up to a minute listing; do not turn a pause or
	// cancel that arrived meanwhile into a finished file.
	if err := s.splitStopError(j); err != nil {
		return err
	}

	// ------------------------------------------------------------ finalizing
	// manifest.json is written locally only now, after every part has been
	// verified remotely, and is the last object uploaded for this file.
	s.setSplitProgress(j, "finalizing", done)
	if err := split.WriteMeta(dir, m, ""); err != nil {
		return fmt.Errorf("writing manifest for %s: %w", f.name, err)
	}
	cpCtx, cpCancel := context.WithTimeout(context.Background(), splitManifestTimeout)
	err = c.CopyFile(cpCtx, rc.CopyFileIn{
		SrcFs:     dir,
		SrcRemote: manifestName,
		DstFs:     j.AccountID + ":",
		DstRemote: joinRemotePath(remoteParts, manifestName),
	})
	cpCancel()
	if err != nil {
		if stopErr := s.splitStopError(j); stopErr != nil {
			return stopErr
		}
		return fmt.Errorf("uploading manifest for %s: %w", f.name, err)
	}
	if err := s.splitStopError(j); err != nil {
		return err
	}
	return nil
}

// splitManifest builds the manifest skeleton for f. The part layout comes
// from the caller's split options, so the part size picked in Settings is
// actually used; Hash fills in the sizes, hashes and the whole-file digest.
func splitManifest(f splitFile, opts split.Options) split.Manifest {
	count := split.PartCount(f.size, opts)
	chunk := int64(0)
	if count > 0 {
		chunk = split.ChunkSize(f.size, count)
	}
	return split.Manifest{
		Format:    split.Format,
		Name:      f.name,
		Size:      f.size,
		ModTime:   f.modTime.Unix(),
		CreatedBy: "Cloudnest",
		CreatedAt: time.Now().Unix(),
		SourceRel: f.rel,
		PartCount: count,
		ChunkSize: chunk,
	}
}

// splitPartSizes returns the size of every part split.Hash produces for size
// bytes laid out in count parts of at most chunk bytes. It mirrors Hash's
// boundary logic exactly so a remote set can be checked without reading (and
// hashing) the local file. Tests pin it against split.Hash.
func splitPartSizes(size int64, count int, chunk int64) []int64 {
	if size <= 0 || count <= 0 || chunk <= 0 || size < int64(count) {
		return nil
	}
	sizes := make([]int64, 0, count)
	var consumed int64
	for i := 0; i < count; i++ {
		left := size - consumed
		remaining := int64(count - i)
		partSize := chunk
		if partSize > left {
			partSize = left
		}
		if i == count-1 {
			partSize = left
		}
		if left-partSize < remaining-1 {
			partSize = left - (remaining - 1)
		}
		if partSize <= 0 {
			return nil
		}
		sizes = append(sizes, partSize)
		consumed += partSize
	}
	if consumed != size {
		return nil
	}
	return sizes
}

// remoteSplitComplete reports whether remoteParts already holds a complete,
// matching split set for f, so an interrupted backup can skip the file instead
// of re-hashing and re-uploading it. It compares every part's remote size with
// the layout the current file would produce, then reads the tiny manifest
// (operations/getfile) and checks that it describes this exact source: same
// name, size, modification time, part count and chunk size. Any error or
// mismatch means "not complete"; the caller rebuilds the set as before.
func (s *Service) remoteSplitComplete(ctx context.Context, c *rc.Client, j *jobState, f splitFile, m split.Manifest, remoteParts string) bool {
	sizes := splitPartSizes(f.size, m.PartCount, m.ChunkSize)
	if len(sizes) == 0 {
		return false
	}
	if err := s.splitStopError(j); err != nil {
		return false
	}

	listCtx, listCancel := context.WithTimeout(context.Background(), splitListTimeout)
	entries, err := c.List(listCtx, j.AccountID+":", remoteParts, rc.ListOpt{})
	listCancel()
	if err != nil {
		return false
	}
	remote := make(map[string]int64, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			remote[e.Name] = e.Size
		}
	}
	if remote[manifestName] <= 0 {
		return false
	}
	for i, size := range sizes {
		name := fmt.Sprintf("part-%04d.bin", i+1)
		if got, ok := remote[name]; !ok || got != size {
			return false
		}
	}

	// The part names and sizes are not enough on their own: a file that changed
	// with the same size would otherwise keep its stale backup forever. The
	// manifest records the source modification time.
	//
	// operations/getfile returns the object's bytes in the JSON "result" field;
	// it does not take an "out" path and writes no local file (verified against
	// rclone 1.70/1.76).
	var resp struct {
		Result string `json:"result"`
	}
	getCtx, getCancel := context.WithTimeout(context.Background(), splitRCCallTimeout)
	err = c.Call(getCtx, "operations/getfile", map[string]any{
		"fs":     j.AccountID + ":",
		"remote": joinRemotePath(remoteParts, manifestName),
	}, &resp)
	getCancel()
	if err != nil || resp.Result == "" {
		return false
	}
	var remoteManifest split.Manifest
	if err := json.Unmarshal([]byte(resp.Result), &remoteManifest); err != nil {
		return false
	}
	if err := split.ValidateManifest(remoteManifest); err != nil {
		return false
	}
	if remoteManifest.Name != f.name || remoteManifest.Size != f.size || remoteManifest.ModTime != f.modTime.Unix() {
		return false
	}
	if remoteManifest.PartCount != m.PartCount || remoteManifest.ChunkSize != m.ChunkSize {
		return false
	}
	for i, size := range sizes {
		if remoteManifest.Parts[i].Size != size {
			return false
		}
	}
	return true
}

// uploadSplitParts stages and uploads every part of one file. A single
// producer stages parts to disk while up to workers copyfile jobs upload the
// parts that are ready, so staging and uploading overlap instead of running as
// two separate passes. Every upload joins the backup's rclone group, which
// keeps core/stats reporting speed and bytes for the whole backup.
func (s *Service) uploadSplitParts(ctx context.Context, c *rc.Client, j *jobState, f splitFile, dir, remoteParts string, m split.Manifest, workers, attempts int) error {
	if len(m.Parts) == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(m.Parts) {
		workers = len(m.Parts)
	}

	// copyfile needs the destination directory to exist. The old sync/copy
	// created it as a side effect; per-part uploads have to ask for it.
	mkCtx, mkCancel := context.WithTimeout(context.Background(), splitRCCallTimeout)
	err := c.Mkdir(mkCtx, j.AccountID+":", remoteParts)
	mkCancel()
	if err != nil {
		if stopErr := s.splitStopError(j); stopErr != nil {
			return stopErr
		}
		return fmt.Errorf("creating remote parts directory: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ready := make(chan split.PartInfo)
	errCh := make(chan error, len(m.Parts)+1)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ready)
		for _, p := range m.Parts {
			if err := s.splitStopError(j); err != nil {
				errCh <- err
				cancel()
				return
			}
			// Staged bytes are deliberately not reported here: while uploads
			// run, the group's stats drive the progress bar, and a second
			// writer would make it jump backwards. The callback still watches
			// for pause/cancel so staging a big part aborts promptly.
			if stageErr := split.Stage(ctx, f.path, m, []int{p.Index}, dir, s.splitStopWatcher(j, cancel)); stageErr != nil {
				errCh <- s.splitFileError(j, "staging "+f.name+" part "+strconv.Itoa(p.Index), stageErr)
				cancel()
				return
			}
			select {
			case ready <- p:
			case <-ctx.Done():
				return
			}
		}
	}()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range ready {
				if err := s.copySplitPart(ctx, c, j, dir, remoteParts, p, attempts); err != nil {
					errCh <- err
					cancel()
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	// Cancelling one worker makes its siblings report "stopped"; keep the
	// error that actually explains the failure when there is one.
	var realErr, stopErr error
	for err := range errCh {
		switch {
		case err == nil:
		case errors.Is(err, errSplitStopped) || errors.Is(err, context.Canceled):
			if stopErr == nil {
				stopErr = err
			}
		default:
			if realErr == nil {
				realErr = err
			}
		}
	}
	if realErr != nil {
		return realErr
	}
	if stopErr != nil {
		return errSplitStopped
	}
	return nil
}

// copySplitPart uploads one staged part with operations/copyfile, retrying a
// failed job up to attempts times. Concurrency comes from the caller's worker
// pool.
func (s *Service) copySplitPart(ctx context.Context, c *rc.Client, j *jobState, dir, remoteParts string, p split.PartInfo, attempts int) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		// PauseJob/CancelJob only stop the jobs that exist when they run, so
		// never start a new part upload after the user has stopped the backup.
		if stopErr := s.splitStopError(j); stopErr != nil {
			return stopErr
		}
		if ctx.Err() != nil {
			return errSplitStopped
		}
		if attempt > 0 {
			// rclone's sync/copy used to provide this backoff; per-part
			// copyfile jobs have to do it themselves.
			timer := time.NewTimer(time.Duration(attempt) * 2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errSplitStopped
			case <-timer.C:
			}
			// The state can change during the backoff without cancelling ctx.
			if stopErr := s.splitStopError(j); stopErr != nil {
				return stopErr
			}
			if ctx.Err() != nil {
				return errSplitStopped
			}
		}
		err = s.copySplitPartOnce(ctx, c, j, dir, remoteParts, p)
		if err == nil {
			return nil
		}
		if errors.Is(err, errSplitStopped) || errors.Is(err, context.Canceled) {
			return errSplitStopped
		}
		if stopErr := s.splitStopError(j); stopErr != nil {
			return stopErr
		}
		if ctx.Err() != nil {
			return errSplitStopped
		}
	}
	return err
}

// copySplitPartOnce makes one operations/copyfile attempt for one part.
func (s *Service) copySplitPartOnce(ctx context.Context, c *rc.Client, j *jobState, dir, remoteParts string, p split.PartInfo) error {
	startCtx, startCancel := context.WithTimeout(context.Background(), splitRCCallTimeout)
	jobID, err := c.StartCopyFile(startCtx, rc.CopyFileIn{
		SrcFs:     dir,
		SrcRemote: p.Name,
		DstFs:     j.AccountID + ":",
		DstRemote: joinRemotePath(remoteParts, p.Name),
		Group:     j.Group,
	})
	startCancel()
	if err != nil {
		if stopErr := s.splitStopError(j); stopErr != nil {
			return stopErr
		}
		return fmt.Errorf("starting upload of part %d: %w", p.Index, err)
	}
	waitErr := s.waitSplitJob(ctx, c, j, jobID)
	if waitErr != nil {
		// This job id is not in j.JobIDs, so nothing else will ever stop it:
		// on pause/cancel, a status error or a retry the old job must be
		// stopped or rclone keeps uploading (and a retry would race it).
		s.stopSplitJob(c, jobID)
		if errors.Is(waitErr, errSplitStopped) {
			return waitErr
		}
		return fmt.Errorf("uploading part %d: %w", p.Index, waitErr)
	}
	return nil
}

// stopSplitJob asks rclone to stop one part upload. Failures are best effort:
// the job may already have finished or been stopped with its group.
func (s *Service) stopSplitJob(c *rc.Client, jobID int64) {
	if jobID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), splitRCCallTimeout)
	defer cancel()
	_ = c.JobStop(ctx, jobID)
}

// splitStopWatcher cancels the pipeline when the user pauses or cancels. It
// deliberately ignores the byte count: the group stats own the progress bar
// while uploads run.
func (s *Service) splitStopWatcher(j *jobState, cancel context.CancelFunc) func(int64) {
	return func(int64) {
		if s.splitStopped(j) {
			cancel()
		}
	}
}

// waitSplitJob polls one upload job. Every job/status call gets its own
// timeout; the overall wait is capped at two hours.
func (s *Service) waitSplitJob(ctx context.Context, c *rc.Client, j *jobState, jobID int64) error {
	deadline := time.Now().Add(splitMaxUploadWait)
	for {
		if err := s.splitStopError(j); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("upload timed out after 2 hours")
		}

		callCtx, callCancel := context.WithTimeout(context.Background(), splitRCCallTimeout)
		status, err := c.JobStatus(callCtx, jobID)
		callCancel()
		if err != nil {
			if stopErr := s.splitStopError(j); stopErr != nil {
				return stopErr
			}
			return fmt.Errorf("checking upload status: %w", err)
		}
		if status.Finished {
			if status.Success {
				return nil
			}
			if stopErr := s.splitStopError(j); stopErr != nil {
				return stopErr
			}
			if status.Error != "" {
				return errors.New(status.Error)
			}
			return errors.New("upload failed")
		}

		timer := time.NewTimer(splitPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errSplitStopped
		case <-timer.C:
		}
	}
}

// verifySplitParts compares remote object sizes with the manifest, then
// re-copies only the bad parts. The file is never split again.
func (s *Service) verifySplitParts(ctx context.Context, c *rc.Client, j *jobState, localDir, remoteDir string, m split.Manifest) error {
	var bad []int
	for attempt := 0; attempt <= splitVerifyRetries; attempt++ {
		if err := s.splitStopError(j); err != nil {
			return err
		}

		listCtx, listCancel := context.WithTimeout(context.Background(), splitListTimeout)
		entries, err := c.List(listCtx, j.AccountID+":", remoteDir, rc.ListOpt{})
		listCancel()
		if err != nil {
			if stopErr := s.splitStopError(j); stopErr != nil {
				return stopErr
			}
			return fmt.Errorf("verifying remote parts: %w", err)
		}
		// The list may take a while; do not re-upload into a paused job.
		if stopErr := s.splitStopError(j); stopErr != nil {
			return stopErr
		}

		sizes := make(map[string]int64, len(entries))
		for _, e := range entries {
			if !e.IsDir {
				sizes[e.Name] = e.Size
			}
		}
		bad = bad[:0]
		for _, p := range m.Parts {
			if size, ok := sizes[p.Name]; !ok || size != p.Size {
				bad = append(bad, p.Index)
			}
		}
		if len(bad) == 0 {
			return nil
		}
		if attempt == splitVerifyRetries {
			break
		}

		for _, index := range bad {
			if err := s.splitStopError(j); err != nil {
				return err
			}
			p := m.Parts[index-1]
			cpCtx, cpCancel := context.WithTimeout(context.Background(), splitPartCopyTimeout)
			copyErr := c.CopyFile(cpCtx, rc.CopyFileIn{
				SrcFs:     localDir,
				SrcRemote: p.Name,
				DstFs:     j.AccountID + ":",
				DstRemote: joinRemotePath(remoteDir, p.Name),
			})
			cpCancel()
			if copyErr != nil {
				if stopErr := s.splitStopError(j); stopErr != nil {
					return stopErr
				}
				return fmt.Errorf("re-uploading part %d: %w", index, copyErr)
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("part %d is still missing or the wrong size after %d retries", bad[0], splitVerifyRetries)
	}
	return nil
}

// deleteRemoteManifest removes a stale completion marker. The caller treats
// failures as best effort.
func deleteRemoteManifest(c *rc.Client, accountID, remoteParts string) error {
	ctx, cancel := context.WithTimeout(context.Background(), splitRCCallTimeout)
	defer cancel()
	err := c.DeleteFile(ctx, accountID+":", joinRemotePath(remoteParts, manifestName))
	if err != nil && rc.IsNotFound(err) {
		return nil
	}
	return err
}

// checkSplitSource verifies that the file is still the one which was hashed.
func (s *Service) checkSplitSource(f splitFile) error {
	st, err := os.Stat(f.path)
	if err != nil {
		return errSplitSourceChanged
	}
	if st.Size() != f.size || !st.ModTime().Equal(f.modTime) {
		return errSplitSourceChanged
	}
	return nil
}

// splitFileError maps split.Hash/Stage failures. Context cancellation is a
// user stop, and a changed source gets the product's plain-language message.
func (s *Service) splitFileError(j *jobState, what string, err error) error {
	if stopErr := s.splitStopError(j); stopErr != nil {
		return stopErr
	}
	if splitSourceChanged(err) {
		return errSplitSourceChanged
	}
	return fmt.Errorf("%s: %w", what, err)
}

func splitSourceChanged(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "changed") ||
		strings.Contains(msg, "mismatch") ||
		strings.Contains(msg, "hashed ")
}

// splitProgress returns an onBytes callback which updates the parent job and
// cancels the work when the user pauses or cancels it.
func (s *Service) splitProgress(j *jobState, base int64, cancel context.CancelFunc) func(int64) {
	return func(n int64) {
		s.mu.Lock()
		j.Bytes = base + n
		state := j.State
		s.mu.Unlock()
		if state == "cancelled" || state == "paused" {
			cancel()
		}
	}
}

func (s *Service) setSplitProgress(j *jobState, phase string, bytes int64) {
	s.mu.Lock()
	j.Phase = phase
	j.Bytes = bytes
	s.mu.Unlock()
}

func (s *Service) splitStopped(j *jobState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return j.State == "cancelled" || j.State == "paused"
}

func (s *Service) splitStopError(j *jobState) error {
	if s.splitStopped(j) {
		return errSplitStopped
	}
	return nil
}

func (s *Service) failSplitJob(j *jobState, message string) {
	s.mu.Lock()
	if j.State == "cancelled" || j.State == "paused" {
		s.mu.Unlock()
		return
	}
	j.State = "failed"
	j.Phase = "failed"
	j.Failed = 1
	j.Current = message
	j.Speed = 0
	s.mu.Unlock()
	s.finishJob(j)
}
