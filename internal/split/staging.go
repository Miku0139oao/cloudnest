package split

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// CleanupStaging removes direct child directories of root whose modification
// time is older than olderThan. A missing root is not an error. All removable
// children are attempted; failures are joined.
func CleanupStaging(root string, olderThan time.Duration) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("split: read staging root: %w", err)
	}

	cutoff := time.Now().Add(-olderThan)
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			errs = append(errs, fmt.Errorf("split: stat staging entry %s: %w", entry.Name(), err))
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			errs = append(errs, fmt.Errorf("split: remove staging entry %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(errs...)
}
