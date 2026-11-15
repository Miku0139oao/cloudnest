package services

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloudnest/internal/dto"
	"cloudnest/internal/planner"
	"cloudnest/internal/rc"
)

// defaultDestRoot is the backup folder used when the wizard leaves the
// destination empty.
const defaultDestRoot = "電腦備份"

// destRootOf normalises a backup destination root exactly the way StartBackup
// does, so the mirror preflight lists the same subtree the job will sync.
func destRootOf(root string) string {
	dest := strings.Trim(root, "/ ")
	if dest == "" {
		dest = defaultDestRoot
	}
	return dest
}

// remoteJoin joins an rclone remote directory with a child name using a single
// slash, so an empty parent yields just the child.
func remoteJoin(parent, child string) string {
	parent = strings.Trim(parent, "/")
	child = strings.Trim(child, "/")
	switch {
	case parent == "":
		return child
	case child == "":
		return parent
	}
	return parent + "/" + child
}

// plainRemoteName reports whether base can name a folder on the remote. A
// drive root has no usable base (filepath.Base returns "\" or "."), so a
// mirror plan cannot list the subtree it would sync into.
func plainRemoteName(base string) bool {
	if base == "" || base == "." || base == ".." {
		return false
	}
	return !strings.ContainsAny(base, `/\:`)
}

type rcLister struct {
	c       *rc.Client
	account string
}

// planListTimeout bounds the recursive remote listing. It matches Plan's own
// 90-second budget: the per-call default (30s) would abort a large destination
// subtree long before the plan is out of time.
const planListTimeout = 90 * time.Second

func (l *rcLister) ListRecursive(ctx context.Context, remotePath string) ([]dto.Entry, error) {
	remote := strings.Trim(remotePath, "/")
	entries, err := l.c.List(ctx, l.account+":", remote, rc.ListOpt{Recurse: true, FilesOnly: true},
		rc.WithTimeout(planListTimeout))
	if err != nil {
		// A folder which has never been backed up has no remote subtree yet;
		// rclone answers operations/list with 404 "directory not found". The
		// mirror sync would create that directory, so an empty preflight is
		// correct (and reports no deletions) instead of a hard failure.
		if rc.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	// rclone reports Path relative to the remote's root, including the listed
	// directory ("電腦備份/Pictures/a.jpg" when remote is "電腦備份/Pictures").
	// planner compares these against paths relative to the source folder, so
	// the listed prefix has to be stripped or every file would look foreign.
	prefix := ""
	if remote != "" {
		prefix = remote + "/"
	}
	out := make([]dto.Entry, 0, len(entries))
	for _, e := range entries {
		p := strings.TrimLeft(e.Path, "/")
		if prefix != "" {
			p = strings.TrimPrefix(p, prefix)
		}
		// The listed directory itself (should not appear with FilesOnly) has no
		// local counterpart and is not a file to delete.
		if p == "" || p == remote {
			continue
		}
		out = append(out, dto.Entry{
			Name:    e.Name,
			Path:    p,
			Size:    e.Size,
			ModTime: e.ModTime.UnixNano(),
			Mime:    e.MimeType,
		})
	}
	return out, nil
}

// Plan computes the backup summary and, for mirror mode, the remote files that
// would be deleted. It never mutates anything.
func (s *Service) Plan(req dto.PlanRequest) dto.PlanResult {
	if len(req.SourcePaths) == 0 {
		return dto.PlanResult{Err: &dto.Fail{Code: "not_found", Message: "no source selected"}}
	}
	c, fail := s.client()
	if fail != nil {
		return dto.PlanResult{Err: fail}
	}
	mode := req.Mode
	if mode == "" {
		mode = planner.ModeAdditive
	}

	// A set of individual files (or a single file) is summed directly; mirror
	// semantics only make sense for a folder.
	first := req.SourcePaths[0]
	st, statErr := os.Stat(first)
	isDir := statErr == nil && st.IsDir()
	if len(req.SourcePaths) > 1 || !isDir {
		if mode == planner.ModeMirror {
			return dto.PlanResult{Err: &dto.Fail{Code: "provider_unsupported", Message: "mirror needs a folder"}}
		}
		var files int
		var bytes int64
		for _, p := range req.SourcePaths {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				files++
				bytes += info.Size()
			}
		}
		return dto.PlanResult{Plan: dto.BackupPlan{Files: files, Bytes: bytes}}
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return dto.PlanResult{Err: failFrom(err)}
	}
	// A folder backup lands in destRoot/<source folder> (StartBackup builds
	// dstFs that way), so a mirror plan must inspect exactly that subtree.
	// Listing destRoot itself would count every other backup in the
	// destination as a deletion.
	base := filepath.Base(filepath.Clean(req.SourcePaths[0]))
	if mode == planner.ModeMirror && !plainRemoteName(base) {
		// A drive root has no folder name to sync into; refuse instead of
		// listing a malformed path like "dest/\".
		return dto.PlanResult{Err: &dto.Fail{
			Code:    "provider_unsupported",
			Message: "mirror needs a folder, not a drive root",
		}}
	}
	remotePath := remoteJoin(destRootOf(req.DestRoot), base)
	var remote planner.RemoteLister
	if mode == planner.ModeMirror {
		remote = &rcLister{c: c, account: req.AccountID}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := planner.Plan(ctx, planner.Request{
		LocalRoot:   req.SourcePaths[0],
		RemotePath:  remotePath,
		Mode:        mode,
		Excludes:    req.Excludes,
		MaxEntries:  planner.DefaultMaxEntries,
		TokenSecret: secret,
		TokenTTL:    10 * time.Minute,
	}, nil, remote)
	if err != nil {
		return dto.PlanResult{Err: failFrom(err)}
	}
	return dto.PlanResult{Plan: dto.BackupPlan{
		Files:       res.Files,
		Bytes:       res.Bytes,
		Estimated:   res.Estimated,
		Warnings:    res.Warnings,
		DeleteCount: res.DeleteCount,
		DeleteBytes: res.DeleteBytes,
		Deletions:   res.Deletions,
		Token:       res.Token,
	}}
}
