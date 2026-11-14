// Package settings stores the small JSON settings file in the portable data dir.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cloudnest/internal/dto"
)

type Store struct {
	mu   sync.Mutex
	path string
	data dto.Settings
}

// New loads (or creates) settings in dir.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "settings.json"), data: Default()}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		var loaded dto.Settings
		if json.Unmarshal(raw, &loaded) == nil {
			s.data = merge(Default(), loaded)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func Default() dto.Settings {
	var s dto.Settings
	s.Language = "zh-Hant"
	s.Theme = "system"
	s.Backup.Transfers = 4
	s.Backup.BwLimit = "off"
	s.Backup.AutoRetry = true
	s.Backup.FastFiles = true
	s.Backup.PartSizeMB = 256
	return s
}

func merge(base, over dto.Settings) dto.Settings {
	if over.Language != "" {
		base.Language = over.Language
	}
	if over.Theme != "" {
		base.Theme = over.Theme
	}
	base.Onboarded = over.Onboarded
	if over.Backup.Transfers > 0 {
		base.Backup.Transfers = over.Backup.Transfers
	}
	if over.Backup.BwLimit != "" {
		base.Backup.BwLimit = over.Backup.BwLimit
	}
	base.Backup.AutoRetry = over.Backup.AutoRetry
	base.Backup.FastFiles = over.Backup.FastFiles
	if over.Backup.PartSizeMB > 0 {
		base.Backup.PartSizeMB = over.Backup.PartSizeMB
	}
	base.Advanced = over.Advanced
	// The schedule is a plain struct whose zero value is meaningful, so it is
	// taken as-is from the file instead of field-by-field.
	base.Schedule = over.Schedule
	if over.Window.Width > 0 && over.Window.Height > 0 {
		base.Window = over.Window
	}
	return base
}

func (s *Store) Get() dto.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

func (s *Store) Update(fn func(*dto.Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.data)
	return s.writeLocked()
}

func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked()
}

func (s *Store) Path() string { return s.path }

func (s *Store) writeLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	// Best-effort backup of the previous file, then atomic replace.
	if _, err := os.Stat(s.path); err == nil {
		_ = copyFile(s.path, s.path+".bak")
	}
	if err := os.Rename(tmp, s.path); err != nil {
		// Windows can refuse the rename if the target is locked; retry briefly.
		time.Sleep(120 * time.Millisecond)
		if err2 := os.Rename(tmp, s.path); err2 != nil {
			return fmt.Errorf("write settings: %w", err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
