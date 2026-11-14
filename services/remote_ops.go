package services

// Remote file operations shared by the Files page: rename, move, copy, search,
// details, share links, trash cleanup and backup verification. Each method
// follows the service contract: expected failures are returned in the DTO and
// nothing panics. Methods returning dto.Empty can only log a failure because
// that DTO has no Err field.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloudnest/internal/apperr"
	"cloudnest/internal/dto"
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

	group := "gui-copy-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	transfers := s.defaultTransfers()
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

		ctx, cancel := context.WithTimeout(context.Background(), opsTimeout)
		if st.IsDir {
			_, err = c.StartSync(ctx, "sync/copy", rc.SyncIn{
				SrcFs:     srcFS + remote,
				DstFs:     dstFS + joinRemotePath(dest, remoteBase(remote)),
				Group:     group,
				Transfers: transfers,
				Retries:   s.retries(),
			})
		} else {
			_, err = c.StartCopyFile(ctx, rc.CopyFileIn{
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
		out.Done++
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
	if err := os.Rename(src, filepath.Join(filepath.Dir(src), name)); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	defer cancel()
	entries, err := c.List(ctx, fs, "", rc.ListOpt{Recurse: true})
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	out.Items, out.Truncated = searchRemoteEntries(entries, q, searchResultLimit)
	return out
}

// FileDetail returns metadata for one object plus the hashes the backend can
// provide. An unsupported MD5 is normal (many object stores only keep one
// checksum), so that failure is ignored.
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

	hashCtx, hashCancel := context.WithTimeout(context.Background(), hashTimeout)
	out.SHA256, err = c.HashSumFile(hashCtx, fs, remote, "sha256")
	hashCancel()
	if err != nil {
		out.SHA256 = ""
		out.Err = failFrom(err)
		return out
	}
	hashCtx, hashCancel = context.WithTimeout(context.Background(), hashTimeout)
	out.MD5, _ = c.HashSumFile(hashCtx, fs, remote, "md5")
	hashCancel()
	return out
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
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := c.Cleanup(ctx, fs, ""); err != nil {
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
func (s *Service) VerifyBackup(accountID, localPath, destRoot string) dto.CheckResult {
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

	remote := verifyRemotePath(local, root, st.IsDir())
	src := verifySrcPath(local, st.IsDir())
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	res, err := c.Check(ctx, src, fs+remote)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}

	// success=false is how rclone reports differences over HTTP 200; it is a
	// result, not a transport failure.
	out.Differ = len(res.Differ)
	out.Missing = len(res.MissingOnSrc) + len(res.MissingOnDst)
	out.Status = res.Status
	if out.Status == "" {
		if res.Success {
			out.Status = "success"
		} else {
			out.Status = "differences found"
		}
	}
	if len(res.Differ) > 0 {
		n := len(res.Differ)
		if n > 5 {
			n = 5
		}
		out.Examples = append(out.Examples, res.Differ[:n]...)
	}
	return out
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

// verifySrcPath returns the directory operations/check can compare. Check works
// on trees, so a single file is verified through its parent folder.
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
