package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloudnest/internal/dto"
	"cloudnest/internal/platform"
	"cloudnest/internal/rc"
	"cloudnest/internal/split"
)

// RestoreSplit downloads a split backup, verifies every part, merges it into a
// single file and returns immediately with a job that reports progress in
// Activity.
func (s *Service) RestoreSplit(accountID, partsPath, destDir string) dto.JobRef {
	c, fail := s.client()
	if fail != nil {
		return dto.JobRef{Err: fail}
	}
	if strings.TrimSpace(partsPath) == "" || strings.TrimSpace(destDir) == "" {
		return dto.JobRef{Err: &dto.Fail{Code: "not_found", Message: "missing path"}}
	}
	partsPath = strings.Trim(partsPath, "/")
	name := strings.TrimSuffix(filepath.Base(partsPath), ".cloudbox-part")
	id := fmt.Sprintf("restore-%d", time.Now().UnixNano())
	group := "gui-" + id
	j := &jobState{
		ID: id, Group: group, Kind: "restore", State: "running", Phase: "downloading",
		Title: name, StartedAt: time.Now(), AccountID: accountID, DestRoot: destDir,
	}
	s.mu.Lock()
	s.jobs[id] = j
	s.order = append(s.order, id)
	s.mu.Unlock()

	go s.runRestore(c, j, accountID, partsPath, destDir)
	return dto.JobRef{ID: id, Group: group}
}

func (s *Service) runRestore(c *rc.Client, j *jobState, accountID, partsPath, destDir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	finish := func(state, message string) {
		s.mu.Lock()
		j.State = state
		if state == "failed" {
			j.Failed = 1
			j.Current = message
		}
		s.mu.Unlock()
		s.finishJob(j)
	}

	stagingRoot, err := platform.StagingDir()
	if err != nil {
		finish("failed", err.Error())
		return
	}
	dir := filepath.Join(stagingRoot, "restore-"+j.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		finish("failed", err.Error())
		return
	}
	defer os.RemoveAll(dir)

	jobID, err := c.StartSync(ctx, "sync/copy", rc.SyncIn{
		SrcFs: accountID + ":" + partsPath, DstFs: dir, Group: j.Group, Transfers: 4,
	})
	if err != nil {
		finish("failed", err.Error())
		return
	}
	for {
		js, err := c.JobStatus(ctx, jobID)
		if err != nil {
			finish("failed", err.Error())
			return
		}
		if js.Finished {
			if !js.Success {
				finish("failed", js.Error)
				return
			}
			break
		}
		select {
		case <-ctx.Done():
			finish("failed", "restore timed out")
			return
		case <-time.After(500 * time.Millisecond):
		}
	}

	m, err := split.ReadManifestFile(dir)
	if err != nil {
		finish("failed", "missing or invalid manifest")
		return
	}
	s.mu.Lock()
	j.Title = m.Name
	j.TotalBytes = m.Size
	j.Phase = "verifying"
	s.mu.Unlock()

	indexes := make([]int, 0, m.PartCount)
	for i := 1; i <= m.PartCount; i++ {
		indexes = append(indexes, i)
	}
	bad, err := split.VerifyDir(ctx, dir, m, indexes)
	if err != nil {
		finish("failed", err.Error())
		return
	}
	if len(bad) > 0 {
		finish("failed", fmt.Sprintf("part %d is missing or corrupt", bad[0]))
		return
	}

	name, err := split.SanitizeName(m.Name)
	if err != nil || name == "" {
		name = "restored"
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		finish("failed", err.Error())
		return
	}
	dest := filepath.Join(destDir, name)
	if _, err := os.Stat(dest); err == nil {
		if newName, exists := split.FindConflicts(destDir, name); exists {
			dest = filepath.Join(destDir, newName)
		}
	}

	s.mu.Lock()
	j.Phase = "merging"
	j.Bytes = 0
	s.mu.Unlock()

	if _, err := split.Merge(ctx, dir, dest, m, func(n int64) {
		s.mu.Lock()
		j.Bytes += n
		s.mu.Unlock()
	}); err != nil {
		finish("failed", err.Error())
		return
	}

	s.mu.Lock()
	j.Phase = "done"
	j.Bytes = m.Size
	s.mu.Unlock()
	finish("done", "")
}
