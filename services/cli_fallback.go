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
