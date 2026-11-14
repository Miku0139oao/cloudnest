package split

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupStaging(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old-job")
	newDir := filepath.Join(root, "new-job")
	file := filepath.Join(root, "keep.txt")
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nested", "part.bin"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldDir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := CleanupStaging(root, time.Hour); err != nil {
		t.Fatalf("CleanupStaging: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old job directory still exists: %v", err)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("new job directory was removed: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("plain file was removed: %v", err)
	}

	if err := CleanupStaging(filepath.Join(root, "missing"), time.Hour); err != nil {
		t.Fatalf("missing root returned error: %v", err)
	}
}
