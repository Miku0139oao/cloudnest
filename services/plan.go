package services

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"time"

	"cloudnest/internal/dto"
	"cloudnest/internal/planner"
	"cloudnest/internal/rc"
)

type rcLister struct {
	c       *rc.Client
	account string
}

func (l *rcLister) ListRecursive(ctx context.Context, remotePath string) ([]dto.Entry, error) {
	remote := strings.Trim(remotePath, "/")
	entries, err := l.c.List(ctx, l.account+":", remote, rc.ListOpt{Recurse: true, FilesOnly: true})
	if err != nil {
		return nil, err
	}
	out := make([]dto.Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, dto.Entry{
			Name:    e.Name,
			Path:    e.Path,
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
	var remote planner.RemoteLister
	if mode == planner.ModeMirror {
		remote = &rcLister{c: c, account: req.AccountID}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := planner.Plan(ctx, planner.Request{
		LocalRoot:   req.SourcePaths[0],
		RemotePath:  strings.Trim(req.DestRoot, "/"),
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
