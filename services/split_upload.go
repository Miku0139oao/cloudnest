// Split upload support for StartBackup.
//
// The remote layout and the local staging primitives live in internal/split.
// This file glues them to rclone's RC API: it walks the sources, hashes and
// stages large files, uploads each parts directory, verifies the remote sizes,
// retries individual parts and finally uploads manifest.json.
package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
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
}

func (p *splitBackupPlan) totalBytes() int64 {
	var n int64
	for _, f := range p.files {
		n += f.size
	}
	return n
}

// splitOptionsFor returns the split options for a backup mode, applying the
// part size the user picked in Settings (in MiB; <= 0 keeps the default).
// Mirror mode is deliberately excluded: sync/sync would delete the remote
// .cloudbox-part directories because they do not exist in the local source tree.
func splitOptionsFor(mode string, partSizeMB int) split.Options {
	opts := split.DefaultOptions()
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
	plan := &splitBackupPlan{sources: make([]splitSourcePlan, len(sources))}
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
	filter := map[string]any{"MaxSize": strconv.FormatInt(maxSize, 10)}
	if len(excludes) > 0 {
		filter["Exclude"] = append([]string(nil), excludes...)
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
	for i, f := range plan.files {
		if s.splitStopped(j) {
			// CancelJob/PauseJob owns the state and has already recorded it.
			return
		}
		err := s.uploadSplitFile(ctx, cancel, c, j, jobStaging, i+1, f, done)
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

// uploadSplitFile runs hash -> stage -> upload -> verify -> manifest for one
// file. The file's staging directory is always removed before returning.
func (s *Service) uploadSplitFile(ctx context.Context, cancel context.CancelFunc, c *rc.Client, j *jobState, jobStaging string, seq int, f splitFile, done int64) error {
	dir := filepath.Join(jobStaging, strconv.Itoa(seq))
	defer os.RemoveAll(dir)

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
	m := split.Manifest{
		Format:    split.Format,
		Name:      f.name,
		ModTime:   f.modTime.Unix(),
		CreatedBy: "Cloudnest",
		CreatedAt: time.Now().Unix(),
		SourceRel: f.rel,
	}
	if err := split.Hash(ctx, f.path, &m, s.splitProgress(j, done, cancel)); err != nil {
		return s.splitFileError(j, "hashing "+f.name, err)
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

	// ------------------------------------------------------------- staging
	s.setSplitProgress(j, "staging", done)
	indexes := make([]int, 0, m.PartCount)
	for i := 1; i <= m.PartCount; i++ {
		indexes = append(indexes, i)
	}
	if err := split.Stage(ctx, f.path, m, indexes, dir, s.splitProgress(j, done, cancel)); err != nil {
		return s.splitFileError(j, "staging "+f.name, err)
	}
	if err := s.checkSplitSource(f); err != nil {
		return err
	}

	// ------------------------------------------------------------ uploading
	s.setSplitProgress(j, "uploading", done)
	remoteParts := joinRemotePath(f.destDir, f.partsDir)

	// A manifest is rclone's completion marker. Remove any stale one before
	// touching the parts directory so a failed run cannot leave an old marker
	// claiming the new set is complete. A missing object is expected.
	if err := deleteRemoteManifest(c, j.AccountID, remoteParts); err != nil {
		if s.log != nil {
			s.log.Warn("service: could not clear previous split manifest", "path", remoteParts, "err", err)
		}
	}

	transfers := m.PartCount
	if transfers > 4 {
		transfers = 4
	}
	if transfers < 1 {
		transfers = 1
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), splitRCCallTimeout)
	jobID, err := c.StartSync(startCtx, "sync/copy", rc.SyncIn{
		SrcFs:     dir,
		DstFs:     j.AccountID + ":" + remoteParts,
		Group:     j.Group,
		Transfers: transfers,
	})
	startCancel()
	if err != nil {
		if stopErr := s.splitStopError(j); stopErr != nil {
			return stopErr
		}
		return fmt.Errorf("starting upload of %s: %w", f.name, err)
	}
	if err := s.waitSplitJob(ctx, c, j, jobID); err != nil {
		if errors.Is(err, errSplitStopped) {
			return err
		}
		return fmt.Errorf("uploading %s: %w", f.name, err)
	}

	// ------------------------------------------------------------- verifying
	if err := s.verifySplitParts(ctx, c, j, dir, remoteParts, m); err != nil {
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
	return nil
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
