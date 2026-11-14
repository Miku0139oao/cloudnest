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
			return dto.JobRef{Err: failFrom(err)}
		}
		job.JobIDs = append(job.JobIDs, jobID)
	}

	s.mu.Lock()
	s.jobs[id] = job
	s.order = append(s.order, id)
	s.mu.Unlock()

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
			return dto.JobRef{Err: failFrom(err)}
		}
		job.JobIDs = append(job.JobIDs, jobID)
	}

	s.mu.Lock()
	s.jobs[id] = job
	s.order = append(s.order, id)
	s.mu.Unlock()

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
func (s *Service) ResumeJob(id string) dto.JobRef {
	c, fail := s.client()
	if fail != nil {
		return dto.JobRef{Err: fail}
	}
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "job not found"}}
	}
	if len(j.Sources) == 0 || j.AccountID == "" {
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.Unknown), Message: "job cannot be resumed"}}
	}
	fs, err := accountFS(j.AccountID)
	if err != nil {
		return dto.JobRef{Err: failFrom(err)}
	}

	// kind selects how one plan entry is executed: a directory goes through
	// sync/copy, a single file through copyfile (sync/copy rejects a file as
	// srcFs).
	type plan struct {
		kind      string // "sync" or "copy"
		src, dst  string
		srcRemote string
		dstRemote string
	}
	var plans []plan
	switch j.Kind {
	case "backup", "upload":
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
			jobID, err = c.StartSync(ctx, "sync/copy", rc.SyncIn{
				SrcFs: p.src, DstFs: p.dst, Group: j.Group, Transfers: transfers,
				Retries: s.retries(),
			})
		}
		cancel()
		if err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		newIDs = append(newIDs, jobID)
	}

	s.mu.Lock()
	// Replace the old, stopped job ids: ActiveJobs waits for every id in the
	// list, and a stopped id would otherwise keep the resumed job paused or
	// mark it failed.
	j.JobIDs = newIDs
	j.State = "running"
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
		// A missing manifest is the normal "incomplete" signal. Only the
		// copyfile outcome is authoritative here: cat may be missing from
		// this rclone build, and that must not mask a real copy failure.
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
