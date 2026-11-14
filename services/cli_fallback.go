package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"cloudnest/internal/rc"
)

// aboutViaCLI asks a fresh rclone process for quota. The daemon's own
// operations/about call is the first choice, but Drive throttling or a wedged
// pacer can make it fail while a new process succeeds.
func (s *Service) aboutViaCLI(accountID string) (used, total, free int64, err error) {
	if s.rclone == "" {
		return 0, 0, 0, fmt.Errorf("rclone binary not resolved")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{"about", accountID + ":", "--json", "--config", s.eng.ConfigPath()}
	cmd := exec.CommandContext(ctx, s.rclone, args...)
	addHiddenWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("rclone about: %w", err)
	}

	var raw struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
		Free  int64 `json:"free"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return 0, 0, 0, fmt.Errorf("decode about: %w", err)
	}
	if raw.Total <= 0 {
		return 0, 0, 0, fmt.Errorf("rclone about returned no quota")
	}
	return raw.Used, raw.Total, raw.Free, nil
}

// listViaCLI is the fallback used when the long-lived rclone rcd is wedged or
// its Drive pacer is backing off. A fresh `rclone lsjson` process is
// independent of the daemon's in-memory state and usually succeeds quickly.
func (s *Service) listViaCLI(accountID, remote string) ([]rc.Entry, error) {
	if s.rclone == "" {
		return nil, fmt.Errorf("rclone binary not resolved")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	target := accountID + ":" + strings.Trim(remote, "/")
	args := []string{"lsjson", target, "--max-depth", "1", "--config", s.eng.ConfigPath()}
	cmd := exec.CommandContext(ctx, s.rclone, args...)
	addHiddenWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rclone lsjson: %w", err)
	}

	var raw []struct {
		Path     string    `json:"Path"`
		Name     string    `json:"Name"`
		Size     int64     `json:"Size"`
		ModTime  time.Time `json:"ModTime"`
		IsDir    bool      `json:"IsDir"`
		MimeType string    `json:"MimeType"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode lsjson: %w", err)
	}
	entries := make([]rc.Entry, 0, len(raw))
	for _, e := range raw {
		entries = append(entries, rc.Entry{
			Path:     e.Path,
			Name:     e.Name,
			Size:     e.Size,
			ModTime:  e.ModTime,
			IsDir:    e.IsDir,
			MimeType: e.MimeType,
		})
	}
	return entries, nil
}
