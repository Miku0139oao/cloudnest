package services

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"cloudnest/internal/rc"
)

func TestNormalizeFileName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "plain", in: "report.pdf", want: "report.pdf"},
		{name: "trims spaces", in: "  report.pdf  ", want: "report.pdf"},
		{name: "unicode", in: "résumé.txt", want: "résumé.txt"},
		{name: "empty", in: "", wantErr: true},
		{name: "spaces only", in: "   ", wantErr: true},
		{name: "slash", in: "a/b.txt", wantErr: true},
		{name: "backslash", in: `a\b.txt`, wantErr: true},
		{name: "colon", in: "remote:file.txt", wantErr: true},
		{name: "dot", in: ".", wantErr: true},
		{name: "dot dot", in: "..", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, fail := normalizeFileName(tc.in)
			if tc.wantErr {
				if fail == nil {
					t.Fatalf("normalizeFileName(%q) = %q, want failure", tc.in, got)
				}
				if fail.Message != "invalid file name" {
					t.Fatalf("failure message = %q, want %q", fail.Message, "invalid file name")
				}
				return
			}
			if fail != nil {
				t.Fatalf("normalizeFileName(%q) failed: %+v", tc.in, fail)
			}
			if got != tc.want {
				t.Fatalf("normalizeFileName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSplitPartsPath(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		isSet bool
	}{
		{
			name: "part inside split directory",
			in:   "backups/video.mp4.cloudbox-part/part-0001.bin",
			want: "backups/video.mp4.cloudbox-part", isSet: true,
		},
		{
			name: "split directory itself", in: "backups/video.mp4.cloudbox-part",
			want: "backups/video.mp4.cloudbox-part", isSet: true,
		},
		{
			name: "trailing slash", in: "backups/video.mp4.cloudbox-part/",
			want: "backups/video.mp4.cloudbox-part", isSet: true,
		},
		{
			name: "root split directory", in: ".cloudbox-part/part-0001.bin",
			want: ".cloudbox-part", isSet: true,
		},
		{name: "ordinary file", in: "backups/video.mp4"},
		{name: "name contains suffix only as text", in: "notes.cloudbox-part.txt"},
		{name: "empty", in: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := splitPartsPath(tc.in)
			if ok != tc.isSet {
				t.Fatalf("splitPartsPath(%q) ok = %v, want %v", tc.in, ok, tc.isSet)
			}
			if got != tc.want {
				t.Fatalf("splitPartsPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSearchRemoteEntries(t *testing.T) {
	mod := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []rc.Entry{
		{Path: "docs/report.pdf", Name: "report.pdf", Size: 10, ModTime: mod},
		{Path: "photos/cat.jpg", Name: "cat.jpg", Size: 20},
		{Path: "backups/video.mp4.cloudbox-part/part-0001.bin", Name: "part-0001.bin", Size: 5},
		{Path: "backups/video.mp4.cloudbox-part", Name: "video.mp4.cloudbox-part", IsDir: true, Size: 99, ModTime: mod.Add(time.Hour)},
		{Path: "backups/video.mp4.cloudbox-part/part-0002.bin", Name: "part-0002.bin", Size: 6},
	}

	t.Run("case insensitive name match", func(t *testing.T) {
		hits, truncated := searchRemoteEntries(entries, "REPORT", 200)
		if truncated {
			t.Fatal("unexpected truncation")
		}
		if len(hits) != 1 || hits[0].Path != "docs/report.pdf" {
			t.Fatalf("hits = %+v", hits)
		}
	})

	t.Run("path match", func(t *testing.T) {
		hits, _ := searchRemoteEntries(entries, "photos/", 200)
		if len(hits) != 1 || hits[0].Name != "cat.jpg" {
			t.Fatalf("hits = %+v", hits)
		}
	})

	t.Run("split parts collapse to one hit", func(t *testing.T) {
		hits, truncated := searchRemoteEntries(entries, "video", 200)
		if truncated {
			t.Fatal("unexpected truncation")
		}
		if len(hits) != 1 {
			t.Fatalf("hits = %+v", hits)
		}
		hit := hits[0]
		if !hit.IsSplit || hit.Name != "video.mp4" {
			t.Fatalf("hit = %+v", hit)
		}
		if hit.Path != "backups/video.mp4.cloudbox-part" {
			t.Fatalf("path = %q", hit.Path)
		}
		// The real directory entry upgrades the placeholder created from a
		// part file, even though it appears later in the listing.
		if hit.Size != 99 || !hit.ModTime.Equal(mod.Add(time.Hour)) {
			t.Fatalf("metadata not upgraded: %+v", hit)
		}
	})

	t.Run("split matches parts directory path", func(t *testing.T) {
		hits, _ := searchRemoteEntries(entries, "cloudbox", 200)
		if len(hits) != 1 || !hits[0].IsSplit {
			t.Fatalf("hits = %+v", hits)
		}
	})

	t.Run("empty query", func(t *testing.T) {
		hits, truncated := searchRemoteEntries(entries, "   ", 200)
		if hits != nil || truncated {
			t.Fatalf("hits = %+v, truncated = %v", hits, truncated)
		}
	})

	t.Run("limit sets truncated", func(t *testing.T) {
		list := []rc.Entry{
			{Path: "a/one", Name: "one"},
			{Path: "a/two", Name: "two"},
		}
		hits, truncated := searchRemoteEntries(list, "o", 1)
		if len(hits) != 1 || !truncated {
			t.Fatalf("hits = %+v, truncated = %v", hits, truncated)
		}
	})

	t.Run("limit reached exactly is not truncated", func(t *testing.T) {
		list := []rc.Entry{
			{Path: "a/one", Name: "one"},
			{Path: "a/two", Name: "two"},
		}
		hits, truncated := searchRemoteEntries(list, "one", 1)
		if len(hits) != 1 || truncated {
			t.Fatalf("hits = %+v, truncated = %v", hits, truncated)
		}
	})
}

func TestVerifyRemotePath(t *testing.T) {
	tests := []struct {
		name     string
		local    string
		destRoot string
		isDir    bool
		want     string
	}{
		{name: "folder keeps its base", local: "/home/u/docs", destRoot: "backups", isDir: true, want: "backups/docs"},
		{name: "folder trailing separator", local: "/home/u/docs/", destRoot: "backups", isDir: true, want: "backups/docs"},
		{name: "folder at account root", local: "/home/u/docs", destRoot: "", isDir: true, want: "docs"},
		{name: "file goes straight to dest root", local: "/home/u/report.pdf", destRoot: "backups", isDir: false, want: "backups"},
		{name: "file at account root", local: "/home/u/report.pdf", destRoot: "", isDir: false, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifyRemotePath(tc.local, tc.destRoot, tc.isDir); got != tc.want {
				t.Fatalf("verifyRemotePath(%q, %q, %v) = %q, want %q", tc.local, tc.destRoot, tc.isDir, got, tc.want)
			}
		})
	}
}

func TestVerifySrcPath(t *testing.T) {
	dir := filepath.Clean("/home/u/docs")
	if got := verifySrcPath("/home/u/docs/", true); got != dir {
		t.Fatalf("directory source = %q, want %q", got, dir)
	}
	parent := filepath.Dir(filepath.Clean("/home/u/report.pdf"))
	if got := verifySrcPath("/home/u/report.pdf", false); got != parent {
		t.Fatalf("file source = %q, want %q", got, parent)
	}
}

func TestSameVolume(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("volume roots only differ on Windows")
	}
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "same drive", a: `C:\one`, b: `C:\two`, want: true},
		{name: "case insensitive drive", a: `C:\one`, b: `c:\two`, want: true},
		{name: "different drives", a: `C:\one`, b: `D:\two`, want: false},
		{name: "different unc shares", a: `\\a\share\one`, b: `\\b\share\two`, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameVolume(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameVolume(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestPublicLinkUnsupported(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "rclone wording", err: errors.New("backend doesn't support public links"), want: true},
		{name: "alternative wording", err: errors.New("public links are not supported"), want: true},
		{name: "unrelated failure", err: errors.New("quota exceeded"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicLinkUnsupported(tc.err); got != tc.want {
				t.Fatalf("publicLinkUnsupported(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestUnsupportedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "not supported", err: errors.New("operation not supported"), want: true},
		{name: "unsupported", err: errors.New("unsupported backend"), want: true},
		{name: "doesn't support", err: errors.New("drive doesn't support cleanup"), want: true},
		{name: "unrelated", err: errors.New("quota exceeded"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unsupportedError(tc.err); got != tc.want {
				t.Fatalf("unsupportedError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
