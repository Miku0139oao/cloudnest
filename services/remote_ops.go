package services

// Remote file operations shared by the Files page: rename, move, copy, search,
// details, share links, trash cleanup and backup verification. Each method
// follows the service contract: expected failures are returned in the DTO and
// nothing panics. Methods returning dto.Empty can only log a failure because
// that DTO has no Err field.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloudnest/internal/apperr"
	"cloudnest/internal/dto"
	"cloudnest/internal/planner"
	"cloudnest/internal/rc"
)

const (
	// searchResultLimit keeps a recursive search response small enough for the
	// UI; reaching it sets Truncated.
	searchResultLimit = 200
	// searchTimeout is larger than opsTimeout because a recursive list walks
	// the whole account tree.
	searchTimeout = 60 * time.Second
	// hashTimeout covers server-side hashing of a large object.
	hashTimeout = 120 * time.Second
	// cleanupTimeout covers a trash purge that may delete many objects.
	cleanupTimeout = 2 * time.Minute
	// checkTimeout bounds operations/check, which reads both trees in full.
	checkTimeout = 2 * time.Hour
	// splitDirSuffix marks a split upload's parts directory.
	splitDirSuffix = ".cloudbox-part"
)

// RenameCloud renames one object inside its current folder (movefile with the
// same source and destination fs).
func (s *Service) RenameCloud(accountID, path, newName string) dto.Empty {
	name, f := normalizeFileName(newName)
	if f != nil {
		return s.logFail("rename", f)
	}
	fs, err := accountFS(accountID)
	if err != nil {
		return s.logFail("rename", failFrom(err))
	}
	c, f := s.client()
	if f != nil {
		return s.logFail("rename", f)
	}
	remote, err := cleanRemotePath(path)
	if err != nil {
		return s.logFail("rename", failFrom(err))
	}
	if isSplitPath(remote) {
		return s.logFail("rename", &dto.Fail{
			Code:    string(apperr.ProviderUnsupported),
			Message: "split uploads cannot be renamed",
		})
	}

	dir := remoteDir(remote)
	target := joinRemotePath(dir, name)
	// operations/movefile replaces an existing destination object, so refuse
	// the rename instead of silently losing the file that already has that
	// name. A case-only rename of the same object is allowed.
	if target != remote {
		if f := remoteDestinationConflict(c, fs, remote, target); f != nil {
			return s.logFail("rename", f)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
	defer cancel()
	err = c.MoveFile(ctx, rc.CopyFileIn{
		SrcFs: fs + dir, SrcRemote: remoteBase(remote),
		DstFs: fs + dir, DstRemote: name,
	})
	if err != nil {
		return s.logFail("rename", failFrom(err))
	}
	return dto.Empty{}
}

// MoveCloud moves each path into destDir. Folders are reported as failed until
// a recursive move is implemented, rather than silently skipped.
func (s *Service) MoveCloud(accountID string, paths []string, destDir string) dto.OpResult {
	var out dto.OpResult
	fs, err := accountFS(accountID)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	c, f := s.client()
	if f != nil {
		out.Err = f
		return out
	}
	dest, err := cleanRemotePathAllowEmpty(destDir)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}

	var firstFail *dto.Fail
	record := func(fail *dto.Fail) {
		if firstFail == nil {
			firstFail = fail
		}
	}
	for _, raw := range paths {
		remote, err := cleanRemotePath(raw)
		if err != nil {
			out.Failed = append(out.Failed, raw)
			record(failFrom(err))
			continue
		}
		if isSplitPath(remote) {
			out.Failed = append(out.Failed, raw)
			record(&dto.Fail{
				Code:    string(apperr.ProviderUnsupported),
				Message: "split uploads cannot be moved",
			})
			continue
		}
		st, err := statRemote(c, fs, remote)
		if err != nil {
			out.Failed = append(out.Failed, raw)
			record(failFrom(err))
			continue
		}
		if st == nil {
			out.Failed = append(out.Failed, raw)
			record(&dto.Fail{Code: string(apperr.NotFound), Message: "not found"})
			continue
		}
		if st.IsDir {
			out.Failed = append(out.Failed, raw)
			record(&dto.Fail{
				Code:    string(apperr.ProviderUnsupported),
				Message: "folders cannot be moved yet",
			})
			continue
		}

		target := joinRemotePath(dest, remoteBase(remote))
		// Like rename: a move must not replace an object that already has the
		// destination name. Moving into the current folder is a no-op, and a
		// case-only path change of the same object is allowed.
		if target != remote {
			if f := remoteDestinationConflict(c, fs, remote, target); f != nil {
				out.Failed = append(out.Failed, raw)
				record(f)
				continue
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
		err = c.MoveFile(ctx, rc.CopyFileIn{
			SrcFs: fs + remoteDir(remote), SrcRemote: remoteBase(remote),
			DstFs: fs + dest, DstRemote: remoteBase(remote),
		})
		cancel()
		if err != nil {
			out.Failed = append(out.Failed, raw)
			record(failFrom(err))
			continue
		}
		out.Done++
	}
	out.Err = firstFail
	return out
}

// CopyCloud starts one asynchronous transfer per path. Files use copyfile and
// folders use sync/copy, all in one group so the Activity page can show and
// stop the whole batch together.
func (s *Service) CopyCloud(srcAccount string, paths []string, dstAccount, destDir string) dto.OpResult {
	var out dto.OpResult
	srcFS, err := accountFS(srcAccount)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	dstFS, err := accountFS(dstAccount)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	c, f := s.client()
	if f != nil {
		out.Err = f
		return out
	}
	dest, err := cleanRemotePathAllowEmpty(destDir)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}

	now := time.Now().UnixNano()
	group := "gui-copy-" + strconv.FormatInt(now, 10)
	transfers := s.defaultTransfers()
	jobIDs := make([]int64, 0, len(paths))
	title := ""
	var firstFail *dto.Fail
	record := func(fail *dto.Fail) {
		if firstFail == nil {
			firstFail = fail
		}
	}
	for _, raw := range paths {
		remote, err := cleanRemotePath(raw)
		if err != nil {
			out.Failed = append(out.Failed, raw)
			record(failFrom(err))
			continue
		}
		if isSplitPath(remote) {
			out.Failed = append(out.Failed, raw)
			record(&dto.Fail{
				Code:    string(apperr.ProviderUnsupported),
				Message: "split uploads are folders of parts and cannot be copied",
			})
			continue
		}
		st, err := statRemote(c, srcFS, remote)
		if err != nil {
			out.Failed = append(out.Failed, raw)
			record(failFrom(err))
			continue
		}
		if st == nil {
			out.Failed = append(out.Failed, raw)
			record(&dto.Fail{Code: string(apperr.NotFound), Message: "not found"})
			continue
		}

		// sync/copy and operations/copyfile replace an existing destination
		// object (or merge into an existing folder). Refuse like Move/Rename
		// instead of silently losing the older copy.
		target := joinRemotePath(dest, remoteBase(remote))
		if f := copyDestinationConflict(c, srcFS, dstFS, remote, target); f != nil {
			out.Failed = append(out.Failed, raw)
			record(f)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
		var jobID int64
		if st.IsDir {
			jobID, err = c.StartSync(ctx, "sync/copy", rc.SyncIn{
				SrcFs:     srcFS + remote,
				DstFs:     dstFS + joinRemotePath(dest, remoteBase(remote)),
				Group:     group,
				Transfers: transfers,
				Retries:   s.retries(),
			})
		} else {
			jobID, err = c.StartCopyFile(ctx, rc.CopyFileIn{
				SrcFs: srcFS + remoteDir(remote), SrcRemote: remoteBase(remote),
				DstFs: dstFS + dest, DstRemote: remoteBase(remote),
				Group: group,
			})
		}
		cancel()
		if err != nil {
			out.Failed = append(out.Failed, raw)
			record(failFrom(err))
			continue
		}
		jobIDs = append(jobIDs, jobID)
		if title == "" {
			title = remoteBase(remote)
		}
		out.Done++
	}
	// Register the batch like Upload/Download so ActiveJobs can report
	// progress and PauseJob/CancelJob can stop it. No request is stored
	// (journalStart gets an empty PlanRequest) because a cross-account copy
	// has no resume plan.
	if len(jobIDs) > 0 {
		job := &jobState{
			ID: "job-" + strconv.FormatInt(now, 10), Group: group,
			JobIDs: jobIDs, Kind: "copy", State: "running", StartedAt: time.Now(),
			Title: title, DestRoot: dest, AccountID: dstAccount, Sources: paths,
		}
		s.mu.Lock()
		s.jobs[job.ID] = job
		s.order = append(s.order, job.ID)
		s.mu.Unlock()
		s.journalStart(dto.Record{
			ID: job.ID, Kind: job.Kind, State: "running", Title: job.Title,
			StartedAt: job.StartedAt.Unix(), Files: job.Files,
		}, dto.PlanRequest{})
	}
	out.Err = firstFail
	return out
}

// MoveLocal moves local files or folders into destDir. A cross-volume rename
// cannot succeed on Windows, so it is rejected before os.Rename is attempted.
func (s *Service) MoveLocal(paths []string, destDir string) dto.OpResult {
	var out dto.OpResult
	dest := strings.TrimSpace(destDir)
	if dest == "" {
		out.Err = &dto.Fail{Code: string(apperr.NotFound), Message: "destination folder is required"}
		return out
	}

	var firstFail *dto.Fail
	record := func(fail *dto.Fail) {
		if firstFail == nil {
			firstFail = fail
		}
	}
	for _, raw := range paths {
		src := strings.TrimSpace(raw)
		if src == "" {
			out.Failed = append(out.Failed, raw)
			record(&dto.Fail{Code: string(apperr.NotFound), Message: "path is required"})
			continue
		}
		src = filepath.Clean(src)
		if !sameVolume(src, dest) {
			out.Failed = append(out.Failed, raw)
			record(&dto.Fail{
				Code:    string(apperr.Unknown),
				Message: "cannot move between disks",
			})
			continue
		}
		target := filepath.Join(dest, filepath.Base(src))
		// os.Rename replaces an existing destination on Windows and Unix, so
		// a move must refuse a name that is already taken instead of silently
		// losing the older file.
		if taken, err := destinationTaken(src, target); err != nil {
			out.Failed = append(out.Failed, raw)
			record(failFrom(err))
			continue
		} else if taken {
			out.Failed = append(out.Failed, raw)
			record(destinationConflict(filepath.Base(src)))
			continue
		}
		if err := os.Rename(src, target); err != nil {
			out.Failed = append(out.Failed, raw)
			record(failFrom(err))
			continue
		}
		out.Done++
	}
	out.Err = firstFail
	return out
}

// RenameLocal renames a local file or folder in place.
func (s *Service) RenameLocal(path, newName string) dto.Empty {
	name, f := normalizeFileName(newName)
	if f != nil {
		return s.logFail("rename local", f)
	}
	src := strings.TrimSpace(path)
	if src == "" {
		return s.logFail("rename local", &dto.Fail{Code: string(apperr.NotFound), Message: "path is required"})
	}
	src = filepath.Clean(src)
	target := filepath.Join(filepath.Dir(src), name)
	if taken, err := destinationTaken(src, target); err != nil {
		return s.logFail("rename local", failFrom(err))
	} else if taken {
		return s.logFail("rename local", destinationConflict(name))
	}
	if err := os.Rename(src, target); err != nil {
		return s.logFail("rename local", failFrom(err))
	}
	return dto.Empty{}
}

// SearchCloud recursively lists accountID and returns case-insensitive name or
// path matches. A split upload is reported once, as its parts directory, so the
// hundreds of part files do not fill the result list.
func (s *Service) SearchCloud(accountID, query string) dto.SearchResult {
	var out dto.SearchResult
	q := strings.TrimSpace(query)
	if q == "" {
		return out
	}
	fs, err := accountFS(accountID)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	c, f := s.client()
	if f != nil {
		out.Err = f
		return out
	}
	// rc.Call caps every request at its 30s default, so the recursive listing
	// passes its own budget explicitly.
	var resp struct {
		List []rc.Entry `json:"list"`
	}
	err = c.Call(context.Background(), "operations/list", map[string]any{
		"fs": fs, "remote": "", "opt": rc.ListOpt{Recurse: true},
	}, &resp, rc.WithTimeout(searchTimeout))
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	entries := resp.List
	out.Items, out.Truncated = searchRemoteEntries(entries, q, searchResultLimit)
	return out
}

// FileDetail returns metadata for one object plus any hashes the backend can
// provide. Hashes are best effort: a backend without a native hash leaves that
// field empty instead of failing the whole request.
func (s *Service) FileDetail(accountID, path string) dto.FileDetail {
	var out dto.FileDetail
	fs, err := accountFS(accountID)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	c, f := s.client()
	if f != nil {
		out.Err = f
		return out
	}
	remote, err := cleanRemotePath(path)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	st, err := statRemote(c, fs, remote)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	if st == nil {
		out.Err = &dto.Fail{Code: string(apperr.NotFound), Message: "not found"}
		return out
	}

	out.Path = remote
	out.Name = st.Name
	if out.Name == "" {
		out.Name = remoteBase(remote)
	}
	out.Size = st.Size
	out.ModTime = st.ModTime
	out.IsDir = st.IsDir
	if st.IsDir {
		return out
	}

	// Hashes are optional detail, not part of the metadata: many backends
	// (Google Drive, OneDrive, S3, WebDAV) have no native SHA-256 and older
	// rclone builds lack operations/hashsumfile (rc.ErrHashSumFileUnsupported).
	// The stat above is the only fatal step; an unavailable hash is logged and
	// left empty so the dialog still shows name, size and modification time.
	out.SHA256 = s.hashFileOptional(c, fs, remote, "sha256")
	out.MD5 = s.hashFileOptional(c, fs, remote, "md5")
	return out
}

// hashFileOptional returns one hash, or "" when the backend or rclone build
// cannot provide it.
func (s *Service) hashFileOptional(c *rc.Client, fs, remote, hashType string) string {
	ctx, cancel := context.WithTimeout(context.Background(), hashTimeout)
	defer cancel()
	h, err := c.HashSumFile(ctx, fs, remote, hashType, rc.WithTimeout(hashTimeout))
	if err != nil {
		if s.log != nil {
			s.log.Debug("service: file hash unavailable", "hash", hashType, "err", err)
		}
		return ""
	}
	return h
}

// ShareLink asks the backend for a public link. Backends without public-link
// support get a stable explanation instead of a generic provider error.
func (s *Service) ShareLink(accountID, path string) dto.ShareResult {
	var out dto.ShareResult
	fs, err := accountFS(accountID)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	c, f := s.client()
	if f != nil {
		out.Err = f
		return out
	}
	remote, err := cleanRemotePath(path)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
	defer cancel()
	out.URL, err = c.PublicLink(ctx, fs, remote)
	if err != nil {
		if publicLinkUnsupported(err) {
			classified := apperr.Classify(0, err.Error())
			out.Err = &dto.Fail{
				Code:    string(apperr.ProviderUnsupported),
				Message: "this cloud cannot create share links",
				Detail:  classified.Detail,
			}
		} else {
			out.Err = failFrom(err)
		}
	}
	return out
}

// CleanupTrash empties the backend's trash for the account.
func (s *Service) CleanupTrash(accountID string) dto.Empty {
	fs, err := accountFS(accountID)
	if err != nil {
		return s.logFail("cleanup trash", failFrom(err))
	}
	c, f := s.client()
	if f != nil {
		return s.logFail("cleanup trash", f)
	}
	// A trash purge can delete many objects and outlive rc.Call's 30s default.
	err = c.Call(context.Background(), "operations/cleanup", map[string]string{"fs": fs}, nil,
		rc.WithTimeout(cleanupTimeout))
	if err != nil {
		if unsupportedError(err) {
			classified := apperr.Classify(0, err.Error())
			return s.logFail("cleanup trash", &dto.Fail{
				Code:    string(apperr.ProviderUnsupported),
				Message: "this cloud does not support emptying trash",
				Detail:  classified.Detail,
			})
		}
		return s.logFail("cleanup trash", failFrom(err))
	}
	return dto.Empty{}
}

// VerifyBackup compares a local backup source with its remote copy. The mapping
// mirrors the upload layout: a folder is stored under destRoot/<base>, a single
// file directly in destRoot.
//
// VerifyBackup has no request argument, so the excludes the backup applied are
// recovered from the matching job. Without them every intentionally excluded
// file would be reported missing on the destination and a complete backup would
// never verify.
func (s *Service) VerifyBackup(accountID, localPath, destRoot string) dto.CheckResult {
	return s.verifyBackup(accountID, localPath, destRoot, s.verifyExcludesFor(accountID, localPath, destRoot))
}

// verifyExcludesFor returns the exclude patterns of the most recent backup job
// which covered localPath at destRoot in accountID, or nil when no such job is
// known.
//
// A newer backup with no excludes must win over an older one which had them:
// otherwise the older patterns would hide files the newest backup really did
// fail to copy, reporting an incomplete backup as verified. The newest matching
// job therefore decides the filter even when its exclude set is empty.
func (s *Service) verifyExcludesFor(accountID, localPath, destRoot string) []string {
	local := filepath.Clean(localPath)
	dest := strings.Trim(strings.TrimSpace(destRoot), "/")
	s.mu.Lock()
	defer s.mu.Unlock()

	// Newest matching live job wins, with or without excludes.
	var best *jobState
	for _, j := range s.jobs {
		if j == nil || j.Kind != "backup" {
			continue
		}
		if j.AccountID != accountID {
			continue
		}
		if strings.Trim(strings.TrimSpace(j.DestRoot), "/") != dest {
			continue
		}
		if !jobCoversLocal(j.Sources, local) {
			continue
		}
		if best == nil || j.StartedAt.After(best.StartedAt) {
			best = j
		}
	}
	if best != nil {
		return append([]string(nil), best.Excludes...)
	}

	// A restarted app has no live jobs, but the persisted journal still carries
	// the request (including its excludes) of the last backup. Entries are
	// newest-first; choose the newest match, again with or without excludes.
	var newest *dto.JournalEntry
	for i := range s.journalEntries {
		e := &s.journalEntries[i]
		if e.Kind != "backup" || e.Request.AccountID != accountID {
			continue
		}
		if strings.Trim(strings.TrimSpace(e.Request.DestRoot), "/") != dest {
			continue
		}
		if !jobCoversLocal(e.Request.SourcePaths, local) {
			continue
		}
		if newest == nil || e.StartedAt > newest.StartedAt {
			newest = e
		}
	}
	if newest == nil {
		return nil
	}
	return append([]string(nil), newest.Request.Excludes...)
}

// jobCoversLocal reports whether one of a job's sources is local or contains
// it.
func jobCoversLocal(sources []string, local string) bool {
	for _, src := range sources {
		src = filepath.Clean(strings.TrimSpace(src))
		if src == "" {
			continue
		}
		if strings.EqualFold(src, local) {
			return true
		}
		rel, err := filepath.Rel(src, local)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		return true
	}
	return false
}

// verifyBackup is VerifyBackup with the exclude patterns the backup applied.
func (s *Service) verifyBackup(accountID, localPath, destRoot string, excludes []string) dto.CheckResult {
	var out dto.CheckResult
	local := strings.TrimSpace(localPath)
	if local == "" {
		out.Err = &dto.Fail{Code: string(apperr.NotFound), Message: "path is required"}
		return out
	}
	local = filepath.Clean(local)
	st, err := os.Stat(local)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	fs, err := accountFS(accountID)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	root, err := cleanRemotePathAllowEmpty(destRoot)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	c, f := s.client()
	if f != nil {
		out.Err = f
		return out
	}

	if !st.IsDir() {
		if planner.Excluded(filepath.Base(local), excludes) {
			out.Same = 1
			out.Status = "excluded from backup"
			return out
		}
		return s.verifySingleFile(c, fs, root, local, st)
	}
	remoteRoot := verifyRemotePath(local, root, true)
	res, err := checkRemote(c, verifySrcPath(local, true), fs+remoteRoot)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	return s.summarizeCheck(c, fs, local, remoteRoot, res, excludes)
}

// verifySingleFile compares one backed-up file with the object the upload
// wrote. operations/check works on trees, so it used to compare the file's
// whole parent folder against the destination root.
func (s *Service) verifySingleFile(c *rc.Client, fs, destRoot, local string, st os.FileInfo) dto.CheckResult {
	var out dto.CheckResult
	base := filepath.Base(local)
	remote := joinRemotePath(destRoot, base)
	rstat, err := statRemote(c, fs, remote)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	if rstat == nil {
		// FastFiles stores a large file as a parts directory rather than one
		// object. Verify the set from its manifest instead of calling it
		// missing. A real stat failure (transport, permission) must be reported
		// as an error: only a nil entry means "not an object".
		parts, perr := statRemote(c, fs, remote+splitDirSuffix)
		if perr != nil {
			out.Err = failFrom(perr)
			return out
		}
		if parts != nil && parts.IsDir {
			bad, verr := verifyRemoteSplitSet(c, fs, remote+splitDirSuffix, local)
			switch {
			case verr != nil:
				out.Err = failFrom(verr)
			case len(bad) > 0:
				out.Differ = 1
				out.Status = "split upload is incomplete"
				if len(bad) > 5 {
					bad = bad[:5]
				}
				out.Examples = bad
			default:
				out.Same = 1
				out.Status = "split upload verified"
			}
			return out
		}
		out.Missing = 1
		out.Status = "missing on destination"
		out.Examples = []string{base}
		return out
	}
	if rstat.IsDir || rstat.Size != st.Size() {
		out.Differ = 1
		out.Status = "differences found"
		out.Examples = []string{base}
		return out
	}
	out.Same = 1
	if same, compared := compareFileHash(c, filepath.Dir(local), fs, base, remote); compared && !same {
		out.Same = 0
		out.Differ = 1
		out.Status = "hashes differ"
		out.Examples = []string{base}
		return out
	} else if compared {
		out.Status = "success"
	} else {
		out.Status = "identical by size (no common hash)"
	}
	return out
}

// summarizeCheck converts one operations/check reply into the DTO. Split
// uploads live only on the destination (<name>.cloudbox-part directories), so
// their parts and the source file they replace are excluded from the tree
// comparison and each set is verified from its manifest instead. localRoot is
// the local tree operations/check compared; remoteRoot is that same tree's
// path relative to the account (what the reply's paths are relative to).
// excludes are the patterns the backup applied: a file excluded from the upload
// exists locally but not remotely, so it must not be reported missing.
func (s *Service) summarizeCheck(c *rc.Client, fs, localRoot, remoteRoot string, res *rc.CheckResult, excludes []string) dto.CheckResult {
	var out dto.CheckResult

	splitDirs := make(map[string]struct{})
	for _, p := range res.MissingOnSrc {
		if dir, ok := splitPartsPath(checkPath(p)); ok {
			splitDirs[dir] = struct{}{}
		}
	}
	isPart := func(p string) bool {
		_, ok := splitPartsPath(checkPath(p))
		return ok
	}
	keep := func(paths []string) []string {
		out := make([]string, 0, len(paths))
		for _, p := range paths {
			if !isPart(p) {
				out = append(out, p)
			}
		}
		return out
	}

	missingOnSrc := keep(res.MissingOnSrc)
	missingOnDst := make([]string, 0, len(res.MissingOnDst))
	for _, p := range res.MissingOnDst {
		key := checkPath(p)
		// The backup skipped this path, so its absence on the destination is
		// expected rather than a difference.
		if planner.Excluded(key, excludes) {
			continue
		}
		parts := joinRemotePath(remoteDir(key), remoteBase(key)+splitDirSuffix)
		if _, ok := splitDirs[parts]; ok {
			// This source file was uploaded as a split set; verifyRemoteSplitSet
			// below checks it instead of reporting it missing.
			continue
		}
		missingOnDst = append(missingOnDst, p)
	}
	differ := keep(res.Differ)
	checkErrors := keep(res.Error)

	splitSets, badSets, failedSets := 0, 0, 0
	var bad []string
	// A split set which could not be read is a verification failure, not a data
	// difference: a transport or manifest timeout says nothing about the
	// backup's content.
	var verifyErr error
	for parts := range splitDirs {
		// The check reply's paths are relative to the compared destination
		// tree, so the remote manifest/list need remoteRoot in front of them.
		// The source file, by contrast, sits at the same relative path below
		// localRoot.
		remoteParts := joinRemotePath(remoteRoot, parts)
		localSource := filepath.Join(localRoot, filepath.FromSlash(strings.TrimSuffix(parts, splitDirSuffix)))
		problems, err := verifyRemoteSplitSet(c, fs, remoteParts, localSource)
		if err != nil {
			failedSets++
			if verifyErr == nil {
				verifyErr = err
			}
			continue
		}
		if len(problems) > 0 {
			badSets++
			bad = append(bad, problems...)
		} else {
			splitSets++
		}
	}

	out.Status = res.Status
	if out.Status == "" {
		switch {
		case res.Success:
			out.Status = "success"
		case len(splitDirs) > 0:
			out.Status = "split uploads verified"
		default:
			out.Status = "differences found"
		}
	}
	if splitSets > 0 {
		out.Status = fmt.Sprintf("%s (%d split uploads verified)", out.Status, splitSets)
	}

	// Same is not part of rclone's reply; derive it from the local tree so the
	// Compare dialog does not always show zero for an identical backup.
	same := countLocalFilesExcluding(localRoot, excludes) - len(missingOnDst) - len(differ) - badSets - failedSets
	if same < 0 {
		same = 0
	}
	out.Same = same
	out.Differ = len(differ) + badSets
	out.Missing = len(missingOnSrc) + len(missingOnDst)

	// rclone's "differences found" covers the split artifacts it could not
	// match, which the manifest checks above reconciled. When those were the
	// only differences every set verified, so the backup is complete.
	if len(checkErrors) == 0 && verifyErr == nil && out.Differ == 0 && out.Missing == 0 && splitSets > 0 {
		out.Status = "split uploads verified"
	}

	examples := append(append([]string{}, differ...), bad...)
	if len(examples) > 5 {
		examples = examples[:5]
	}
	out.Examples = examples

	// rclone reports files it could not read or hash as errors, not as
	// differences. They were not verified, so the UI must not present the
	// backup as complete.
	out.Errors = len(checkErrors)
	if len(checkErrors) > 0 {
		detail := checkErrors
		if len(detail) > 5 {
			detail = detail[:5]
		}
		out.ErrorExamples = append([]string{}, detail...)
		out.Err = &dto.Fail{
			Code:    string(apperr.Unknown),
			Message: fmt.Sprintf("verification did not complete: %d item(s) could not be checked", len(checkErrors)),
			Detail:  strings.Join(detail, "; "),
		}
	} else if verifyErr != nil {
		out.Err = failFrom(verifyErr)
	} else if !res.Success && out.Differ == 0 && out.Missing == 0 && len(splitDirs) == 0 {
		if len(excludes) > 0 {
			// The source files the backup excluded exist locally but, by
			// design, not remotely, so the check reports them missing and sets
			// success=false. Once they are filtered out nothing is actually
			// missing: the upload copied everything it was asked to. Do not let
			// the raw success bit turn a complete backup into a failure.
			out.Status = "success"
		} else {
			out.Err = &dto.Fail{
				Code:    string(apperr.Unknown),
				Message: "verification did not complete",
				Detail:  out.Status,
			}
		}
	}
	return out
}

// verifyRemoteSplitSet checks one <name>.cloudbox-part directory against its
// manifest. remoteParts is that directory's path relative to fs (the account
// root); localSource is the local file the parts reconstruct. The set is only
// verified when the local source still has the size, modification time and
// content the manifest records, so a changed file of the same size differs.
func verifyRemoteSplitSet(c *rc.Client, fs, remoteParts, localSource string) ([]string, error) {
	m, state, err := readRemoteManifest(c, fs, remoteParts)
	if err != nil {
		return nil, err
	}
	if state != "" {
		return []string{remoteParts + " (manifest " + state + ")"}, nil
	}
	st, err := os.Stat(localSource)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{remoteParts + " (source file not found)"}, nil
		}
		return nil, err
	}
	if m.Size > 0 && st.Size() != m.Size {
		return []string{remoteParts + " (source size differs)"}, nil
	}
	if m.ModTime > 0 && st.ModTime().Unix() != m.ModTime {
		return []string{remoteParts + " (source changed since backup)"}, nil
	}
	if m.SHA256 != "" {
		sum, err := localSHA256(localSource)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(sum, m.SHA256) {
			return []string{remoteParts + " (source content differs)"}, nil
		}
	}

	entries, err := listRemote(c, fs, remoteParts)
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			sizes[e.Name] = e.Size
		}
	}
	var bad []string
	for _, p := range m.Parts {
		if size, ok := sizes[p.Name]; !ok || size != p.Size {
			bad = append(bad, remoteParts+"/"+p.Name)
		}
	}
	return bad, nil
}

// localSHA256 returns the lowercase hex SHA-256 of a local file, streaming so a
// large split source is never read into memory.
func localSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// checkRemote runs operations/check with the full verification budget. rc.Call
// otherwise caps every request at its 30s default, which aborts a comparison
// of a large backup.
func checkRemote(c *rc.Client, srcFs, dstFs string) (*rc.CheckResult, error) {
	var out rc.CheckResult
	err := c.Call(context.Background(), "operations/check",
		map[string]string{"srcFs": srcFs, "dstFs": dstFs}, &out,
		rc.WithTimeout(checkTimeout))
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// compareFileHash compares the first hash both the local file and the remote
// object can produce. A backend without native hashes or an rclone without
// operations/hashsumfile simply yields compared=false.
func compareFileHash(c *rc.Client, localFS, remoteFS, base, remote string) (same, compared bool) {
	for _, hashType := range []string{"sha256", "sha1", "md5"} {
		rh, rok := fileHash(c, remoteFS, remote, hashType)
		if !rok {
			continue
		}
		lh, lok := fileHash(c, localFS, base, hashType)
		if !lok {
			continue
		}
		return strings.EqualFold(lh, rh), true
	}
	return false, false
}

// fileHash returns one hash and whether it is available. Errors (unsupported
// hash type, old rclone, transport failure) mean "not available".
func fileHash(c *rc.Client, fs, remote, hashType string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), hashTimeout)
	defer cancel()
	h, err := c.HashSumFile(ctx, fs, remote, hashType, rc.WithTimeout(hashTimeout))
	if err != nil || h == "" {
		return "", false
	}
	return h, true
}

// checkPath normalises rclone's tree-relative paths to forward slashes.
func checkPath(p string) string {
	return strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
}

// countLocalFiles counts the regular files under root. Errors are ignored; the
// count only feeds the informational "same" total.
func countLocalFiles(root string) int {
	n := 0
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// countLocalFilesExcluding counts the regular files under root which the backup
// would upload: files matching its exclude patterns are skipped just like the
// upload does.
func countLocalFilesExcluding(root string, excludes []string) int {
	if len(excludes) == 0 {
		return countLocalFiles(root)
	}
	n := 0
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if planner.Excluded(rel, excludes) {
			return nil
		}
		n++
		return nil
	})
	return n
}

// ---------------------------------------------------------------- helpers

// normalizeFileName validates a user-supplied object name. Separators are
// rejected because the name is a single remote path segment.
func normalizeFileName(name string) (string, *dto.Fail) {
	n := strings.TrimSpace(name)
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\:`) {
		return "", &dto.Fail{Code: string(apperr.NotFound), Message: "invalid file name"}
	}
	return n, nil
}

// splitPartsPath reports the parts directory containing p, if any. It matches
// both the directory itself and anything below it.
func splitPartsPath(p string) (string, bool) {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "", false
	}
	marker := splitDirSuffix + "/"
	if i := strings.Index(p, marker); i >= 0 {
		return p[:i+len(splitDirSuffix)], true
	}
	if strings.HasSuffix(p, splitDirSuffix) {
		return p, true
	}
	return "", false
}

func isSplitPath(p string) bool {
	_, ok := splitPartsPath(p)
	return ok
}

// searchMatches expects query to be lower-cased already.
func searchMatches(name, path, query string) bool {
	if query == "" {
		return false
	}
	return strings.Contains(strings.ToLower(name), query) ||
		strings.Contains(strings.ToLower(path), query)
}

// searchRemoteEntries filters a recursive listing. Split part files collapse
// into one hit for the parts directory, and a real directory entry upgrades
// that hit's metadata when it appears later.
func searchRemoteEntries(entries []rc.Entry, query string, limit int) ([]dto.SearchHit, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, false
	}
	if limit <= 0 {
		limit = searchResultLimit
	}
	hits := make([]dto.SearchHit, 0, 16)
	splitAt := make(map[string]int)
	truncated := false
	for _, e := range entries {
		if parts, ok := splitPartsPath(e.Path); ok {
			display := splitDisplayName(parts)
			if !searchMatches(display, parts, q) {
				continue
			}
			if idx, seen := splitAt[parts]; seen {
				if e.IsDir && hits[idx].Size == 0 && hits[idx].ModTime.IsZero() {
					hits[idx].Size = e.Size
					hits[idx].ModTime = e.ModTime
				}
				continue
			}
			if len(hits) >= limit {
				truncated = true
				continue
			}
			hit := dto.SearchHit{Path: parts, Name: display, IsSplit: true}
			if e.IsDir {
				hit.Size = e.Size
				hit.ModTime = e.ModTime
			}
			splitAt[parts] = len(hits)
			hits = append(hits, hit)
			continue
		}
		if !searchMatches(e.Name, e.Path, q) {
			continue
		}
		if len(hits) >= limit {
			truncated = true
			continue
		}
		hits = append(hits, dto.SearchHit{
			Path: e.Path, Name: e.Name, Size: e.Size, ModTime: e.ModTime,
		})
	}
	return hits, truncated
}

// verifyRemotePath mirrors the upload layout for a verified backup.
func verifyRemotePath(localPath, destRoot string, isDir bool) string {
	if !isDir {
		return destRoot
	}
	return joinRemotePath(destRoot, filepath.Base(filepath.Clean(localPath)))
}

// verifySrcPath returns the local path operations/check compares. Callers pass
// a folder; a single file is compared object-by-object by verifySingleFile
// instead of through its parent folder.
func verifySrcPath(localPath string, isDir bool) string {
	p := filepath.Clean(localPath)
	if isDir {
		return p
	}
	return filepath.Dir(p)
}

// sameVolume compares the volume roots of two local paths. Resolving to an
// absolute path first avoids comparing "" for a relative source with "D:" for
// an absolute destination.
func sameVolume(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		aa = filepath.Clean(a)
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		bb = filepath.Clean(b)
	}
	return strings.EqualFold(filepath.VolumeName(aa), filepath.VolumeName(bb))
}

// destinationTaken reports whether target already exists and is a different
// file from src. os.Rename would replace such a target.
func destinationTaken(src, target string) (bool, error) {
	tst, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	// Renaming or moving a path onto itself is a no-op, not a conflict.
	if sst, serr := os.Lstat(src); serr == nil && os.SameFile(sst, tst) {
		return false, nil
	}
	return true, nil
}

func destinationConflict(name string) *dto.Fail {
	return &dto.Fail{
		Code:    string(apperr.Unknown),
		Message: "a file named " + name + " already exists in the destination",
	}
}

// remoteDestinationConflict reports a fail when target already holds a
// different object. rclone's operations/movefile replaces the destination
// silently. Renaming or moving a path onto itself is a no-op, and on a
// case-insensitive backend a case-only rename (report.pdf -> Report.pdf) finds
// the source object again, which is not a conflict either. This mirrors
// destinationTaken's os.SameFile check for local paths.
func remoteDestinationConflict(c *rc.Client, fs, src, target string) *dto.Fail {
	if target == src {
		return nil
	}
	st, err := statRemote(c, fs, target)
	if err != nil {
		if rc.IsNotFound(err) {
			return nil
		}
		return failFrom(err)
	}
	if st == nil {
		return nil
	}
	// The target resolved to an object. When only the case differs it may be
	// the source itself; compare the immutable metadata because remote entries
	// have no inode. A genuinely different object has its own size/mod time.
	if strings.EqualFold(target, src) {
		if sst, serr := statRemote(c, fs, src); serr == nil && sameRemoteEntry(sst, st) {
			return nil
		}
	}
	return destinationConflict(remoteBase(target))
}

// copyDestinationConflict reports a fail when target already holds a different
// object. Unlike move/rename, CopyCloud may cross accounts, so the source object
// is not necessarily visible on the destination fs.
func copyDestinationConflict(c *rc.Client, srcFS, dstFS, srcRemote, target string) *dto.Fail {
	if srcFS == dstFS {
		return remoteDestinationConflict(c, dstFS, srcRemote, target)
	}
	st, err := statRemote(c, dstFS, target)
	if err != nil {
		if rc.IsNotFound(err) {
			return nil
		}
		return failFrom(err)
	}
	if st == nil {
		return nil
	}
	return destinationConflict(remoteBase(target))
}

// sameRemoteEntry reports whether two stats describe the same remote object.
func sameRemoteEntry(a, b *rc.Entry) bool {
	if a == nil || b == nil {
		return false
	}
	return a.IsDir == b.IsDir && a.Size == b.Size && a.ModTime.Equal(b.ModTime)
}

// publicLinkUnsupported detects the backend's "no public links" reply, which
// rclone reports as an ordinary error.
func publicLinkUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "support public link") ||
		(strings.Contains(msg, "public link") && strings.Contains(msg, "not supported"))
}

// unsupportedError reports whether a backend lacks the requested operation.
func unsupportedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "doesn't support") ||
		strings.Contains(msg, "does not support")
}

// logFail records a failure for methods whose DTO has no Err field.
func (s *Service) logFail(action string, f *dto.Fail) dto.Empty {
	if s.log != nil && f != nil {
		s.log.Warn("service: "+action+" failed", "code", f.Code, "message", f.Message, "detail", f.Detail)
	}
	return dto.Empty{Err: f}
}
