package services

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cloudnest/internal/dto"
	"cloudnest/internal/engine"
	"cloudnest/internal/rc"
	"cloudnest/internal/split"
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

// ---------------------------------------------------------------- local move

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestMoveLocalPreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "archive")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := filepath.Join(dir, "report.pdf")
	target := filepath.Join(dest, "report.pdf")
	writeTestFile(t, src, "new contents")
	writeTestFile(t, target, "older archive copy")

	out := (&Service{}).MoveLocal([]string{src}, dest)

	if out.Done != 0 {
		t.Fatalf("Done = %d, want 0", out.Done)
	}
	if len(out.Failed) != 1 || out.Failed[0] != src {
		t.Fatalf("Failed = %v, want [%s]", out.Failed, src)
	}
	if out.Err == nil {
		t.Fatal("MoveLocal reported no error for an existing destination")
	}
	if got := readTestFile(t, target); got != "older archive copy" {
		t.Fatalf("destination was overwritten: %q", got)
	}
	if got := readTestFile(t, src); got != "new contents" {
		t.Fatalf("source was changed: %q", got)
	}
}

func TestMoveLocalMovesIntoFreeDestination(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "archive")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := filepath.Join(dir, "report.pdf")
	writeTestFile(t, src, "contents")

	out := (&Service{}).MoveLocal([]string{src}, dest)

	if out.Done != 1 || out.Err != nil {
		t.Fatalf("MoveLocal = %+v", out)
	}
	if got := readTestFile(t, filepath.Join(dest, "report.pdf")); got != "contents" {
		t.Fatalf("destination = %q", got)
	}
}

func TestRenameLocalPreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	target := filepath.Join(dir, "b.txt")
	writeTestFile(t, src, "a contents")
	writeTestFile(t, target, "b contents")

	out := (&Service{}).RenameLocal(src, "b.txt")

	if out.Err == nil {
		t.Fatal("RenameLocal reported success for an existing destination")
	}
	if got := readTestFile(t, target); got != "b contents" {
		t.Fatalf("destination was overwritten: %q", got)
	}
	if got := readTestFile(t, src); got != "a contents" {
		t.Fatalf("source was changed: %q", got)
	}
}

func TestRenameLocalRenamesWhenFree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	writeTestFile(t, src, "contents")

	if out := (&Service{}).RenameLocal(src, "b.txt"); out.Err != nil {
		t.Fatalf("RenameLocal: %+v", out.Err)
	}
	if got := readTestFile(t, filepath.Join(dir, "b.txt")); got != "contents" {
		t.Fatalf("destination = %q", got)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ---------------------------------------------------------------- file detail

// stubEngine serves a fixed rc.Client so FileDetail can be tested without an
// rclone process.
type stubEngine struct{ client *rc.Client }

func (e *stubEngine) Start(context.Context) error   { return nil }
func (e *stubEngine) Stop(context.Context) error    { return nil }
func (e *stubEngine) Restart(context.Context) error { return nil }
func (e *stubEngine) Status() engine.Status         { return engine.Status{} }
func (e *stubEngine) Client() (*rc.Client, error)   { return e.client, nil }
func (e *stubEngine) Subscribe(func(engine.Status)) func() {
	return func() {}
}
func (e *stubEngine) ConfigPath() string      { return "" }
func (e *stubEngine) ApplyRuntime(bool, bool) {}

func TestFileDetailKeepsMetadataWhenHashFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/version":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "v1.76.0", "decomposed": []int{1, 76, 0},
			})
		case "/operations/stat":
			_ = json.NewEncoder(w).Encode(map[string]any{"item": map[string]any{
				"Path": "docs/report.pdf", "Name": "report.pdf",
				"Size": 42, "ModTime": time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
			}})
		case "/operations/hashsumfile":
			var in struct {
				HashType string `json:"hashType"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.HashType == "sha256" {
				// Google Drive/OneDrive/S3 reply like this: no native SHA-256.
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "hash type \"sha256\" not supported", "status": 500,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"hash": "md5value", "hashType": "md5"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{eng: &stubEngine{client: rc.New(server.URL, "", "", nil)}}
	d := s.FileDetail("acct", "docs/report.pdf")

	if d.Err != nil {
		t.Fatalf("FileDetail returned an error: %+v", d.Err)
	}
	if d.Name != "report.pdf" || d.Size != 42 || d.Path != "docs/report.pdf" {
		t.Fatalf("metadata lost: %+v", d)
	}
	if d.SHA256 != "" {
		t.Fatalf("SHA256 = %q, want empty", d.SHA256)
	}
	if d.MD5 != "md5value" {
		t.Fatalf("MD5 = %q, want md5value", d.MD5)
	}
}

func TestFileDetailWithoutHashSupport(t *testing.T) {
	hashCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/version":
			// rclone before 1.73 has no operations/hashsumfile.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "v1.70.3", "decomposed": []int{1, 70, 3},
			})
		case "/operations/stat":
			_ = json.NewEncoder(w).Encode(map[string]any{"item": map[string]any{
				"Path": "docs/report.pdf", "Name": "report.pdf", "Size": 7,
			}})
		case "/operations/hashsumfile":
			hashCalls++
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{eng: &stubEngine{client: rc.New(server.URL, "", "", nil)}}
	d := s.FileDetail("acct", "docs/report.pdf")

	if d.Err != nil {
		t.Fatalf("FileDetail returned an error: %+v", d.Err)
	}
	if d.Name != "report.pdf" || d.Size != 7 {
		t.Fatalf("metadata lost: %+v", d)
	}
	if d.SHA256 != "" || d.MD5 != "" {
		t.Fatalf("hashes = %q/%q, want empty", d.SHA256, d.MD5)
	}
	if hashCalls != 0 {
		t.Fatalf("hashsumfile was called %d times on rclone 1.70", hashCalls)
	}
}

func TestVerifyBackupSingleFileChecksOnlyThatObject(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "report.pdf")
	writeTestFile(t, local, "hello")

	statChecks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/version":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "v1.76.0", "decomposed": []int{1, 76, 0},
			})
		case "/operations/stat":
			var in struct {
				Fs     string `json:"fs"`
				Remote string `json:"remote"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Fs != "acct:" || in.Remote != "backups/report.pdf" {
				t.Errorf("stat looked at %q/%q", in.Fs, in.Remote)
			}
			statChecks++
			_ = json.NewEncoder(w).Encode(map[string]any{"item": map[string]any{
				"Path": "report.pdf", "Name": "report.pdf", "Size": 5,
			}})
		case "/operations/hashsumfile":
			_ = json.NewEncoder(w).Encode(map[string]any{"hash": "abc123", "hashType": "sha256"})
		case "/operations/check":
			t.Error("single-file verification must not compare the parent folder")
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := &Service{eng: &stubEngine{client: rc.New(server.URL, "", "", nil)}}
	res := s.VerifyBackup("acct", local, "backups")

	if res.Err != nil {
		t.Fatalf("VerifyBackup: %+v", res.Err)
	}
	if res.Same != 1 || res.Differ != 0 || res.Missing != 0 {
		t.Fatalf("result = %+v, want 1 same", res)
	}
	if statChecks != 1 {
		t.Fatalf("destination stat calls = %d, want 1", statChecks)
	}
}

func TestVerifyBackupSingleFileSplitSetIsComplete(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "movie.mp4")
	writeTestFile(t, local, "hello")

	f := &splitVerifyFake{
		manifest: mustMarshal(t, testSplitManifest(t, local, 5)),
		parts:    []map[string]any{{"Path": "part-0001.bin", "Name": "part-0001.bin", "Size": 5}},
		statItems: map[string]map[string]any{
			"backups/movie.mp4.cloudbox-part": {
				"Path": "movie.mp4.cloudbox-part", "Name": "movie.mp4.cloudbox-part", "IsDir": true,
			},
		},
	}

	s := &Service{eng: &stubEngine{client: f.client(t)}}
	res := s.VerifyBackup("acct", local, "backups")

	if res.Err != nil {
		t.Fatalf("VerifyBackup: %+v", res.Err)
	}
	if res.Same != 1 || res.Differ != 0 || res.Missing != 0 {
		t.Fatalf("result = %+v, want a complete split set", res)
	}
	if res.Status != "split upload verified" {
		t.Fatalf("status = %q, want split upload verified", res.Status)
	}
	assertSplitManifestFallback(t, f, "acct:", "backups/movie.mp4.cloudbox-part/manifest.json")
}

// splitVerifyFake serves the RC calls split verification makes. operations/cat
// answers 404 like rclone 1.76, which does not implement it, so a test can only
// pass through the operations/copyfile fallback the production code uses.
type splitVerifyFake struct {
	manifest   []byte
	parts      []map[string]any
	statItems  map[string]map[string]any
	checkReply map[string]any
	copyStatus int
	catCalls   int
	copies     []moveCall
	checkSrc   string
	checkDst   string
}

func (f *splitVerifyFake) client(t *testing.T) *rc.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/operations/cat":
			f.catCalls++
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":  `couldn't find method "operations/cat"`,
				"status": http.StatusNotFound,
			})
		case "/operations/copyfile":
			var in moveCall
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode copyfile: %v", err)
			}
			f.copies = append(f.copies, in)
			if f.copyStatus != 0 {
				w.WriteHeader(f.copyStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "copy failed", "status": f.copyStatus,
				})
				return
			}
			// readRemoteManifest copies the manifest into a temporary local
			// directory; a real operations/copyfile writes that file and
			// returns an empty reply.
			dest := filepath.Join(in.DstFs, filepath.FromSlash(in.DstRemote))
			if err := os.WriteFile(dest, f.manifest, 0o644); err != nil {
				t.Errorf("write manifest copy: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case "/operations/stat":
			var in struct {
				Remote string `json:"remote"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(map[string]any{"item": f.statItems[in.Remote]})
		case "/operations/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"list": f.parts})
		case "/operations/check":
			var in struct {
				SrcFs string `json:"srcFs"`
				DstFs string `json:"dstFs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.checkSrc, f.checkDst = in.SrcFs, in.DstFs
			_ = json.NewEncoder(w).Encode(f.checkReply)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return rc.New(srv.URL, "", "", nil)
}

// assertSplitManifestFallback proves the manifest was read through the real
// rclone 1.76 path: operations/cat is absent and operations/copyfile carries the
// account-relative source.
func assertSplitManifestFallback(t *testing.T, f *splitVerifyFake, wantFs, wantRemote string) {
	t.Helper()
	if f.catCalls != 1 {
		t.Fatalf("operations/cat calls = %d, want 1 (rclone 1.76 has no operations/cat)", f.catCalls)
	}
	if len(f.copies) != 1 {
		t.Fatalf("manifest copy calls = %d, want 1", len(f.copies))
	}
	got := f.copies[0]
	if got.SrcFs != wantFs || got.SrcRemote != wantRemote {
		t.Fatalf("manifest copy = %q/%q, want %q/%q", got.SrcFs, got.SrcRemote, wantFs, wantRemote)
	}
	if got.DstRemote != "manifest.json" {
		t.Fatalf("manifest copy destination name = %q, want manifest.json", got.DstRemote)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestVerifyBackupFolderSplitSetIsComplete(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "Photos")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(local, "movie.mp4"), "hello")
	writeTestFile(t, filepath.Join(local, "notes.txt"), "notes")

	m := testSplitManifest(t, filepath.Join(local, "movie.mp4"), 5)
	res := runFolderSplitCheck(t, local, m, true)

	if res.Err != nil {
		t.Fatalf("VerifyBackup: %+v", res.Err)
	}
	if res.Differ != 0 || res.Missing != 0 {
		t.Fatalf("result = %+v, want no differences", res)
	}
	if res.Same != 2 {
		t.Fatalf("Same = %d, want 2", res.Same)
	}
	if res.Status != "split uploads verified" {
		t.Fatalf("status = %q, want split uploads verified", res.Status)
	}
}

func TestVerifyBackupFolderIncompleteSplitSetDiffers(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "Photos")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(local, "movie.mp4"), "hello")
	writeTestFile(t, filepath.Join(local, "notes.txt"), "notes")

	m := testSplitManifest(t, filepath.Join(local, "movie.mp4"), 5)
	res := runFolderSplitCheck(t, local, m, false)

	if res.Err != nil {
		t.Fatalf("VerifyBackup: %+v", res.Err)
	}
	if res.Differ == 0 {
		t.Fatalf("result = %+v, want an incomplete split set to differ", res)
	}
	if len(res.Examples) == 0 {
		t.Fatalf("Examples empty for an incomplete set: %+v", res)
	}
}

// runFolderSplitCheck serves the folder verification of a backup with one
// split source file under destRoot "backups". complete controls whether the
// parts listing contains the manifest's part.
func runFolderSplitCheck(t *testing.T, local string, m split.Manifest, complete bool) dto.CheckResult {
	t.Helper()
	entries := []map[string]any{}
	if complete {
		entries = append(entries, map[string]any{
			"Path": "part-0001.bin", "Name": "part-0001.bin", "Size": 5,
		})
	}
	f := &splitVerifyFake{
		manifest: mustMarshal(t, m),
		parts:    entries,
		checkReply: map[string]any{
			// rclone reports the split directory's parts as missing on the
			// source and the replaced file as missing on the destination.
			"missingOnSrc": []string{"movie.mp4.cloudbox-part/part-0001.bin"},
			"missingOnDst": []string{"movie.mp4"},
			"status":       "2 differences found",
			"success":      false,
		},
	}

	s := &Service{eng: &stubEngine{client: f.client(t)}}
	res := s.VerifyBackup("acct", local, "backups")

	// The check reply is relative to the destination subtree, so the split
	// set must be read below destRoot/<base>, not at the account root.
	if f.checkDst != "acct:backups/Photos" {
		t.Fatalf("check compared %q, want acct:backups/Photos", f.checkDst)
	}
	assertSplitManifestFallback(t, f, "acct:", "backups/Photos/movie.mp4.cloudbox-part/manifest.json")
	return res
}

// testSplitManifest builds a valid one-part manifest from the local source file:
// size, modification time and whole-file hash all describe the file the parts
// reconstruct, as the real uploader records them.
func testSplitManifest(t *testing.T, localPath string, partSizes ...int64) split.Manifest {
	t.Helper()
	data, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read split source: %v", err)
	}
	st, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat split source: %v", err)
	}
	var total int64
	parts := make([]split.PartInfo, len(partSizes))
	for i, partSize := range partSizes {
		parts[i] = split.PartInfo{
			Index:  i + 1,
			Name:   fmt.Sprintf("part-%04d.bin", i+1),
			Size:   partSize,
			SHA256: strings.Repeat("b", 64),
		}
		total += partSize
	}
	if total != int64(len(data)) {
		t.Fatalf("part sizes sum to %d, local file is %d bytes", total, len(data))
	}
	return split.Manifest{
		Format:    split.Format,
		Name:      filepath.Base(localPath),
		Size:      int64(len(data)),
		ModTime:   st.ModTime().Unix(),
		SHA256:    fmt.Sprintf("%x", sha256.Sum256(data)),
		ChunkSize: partSizes[0],
		PartCount: len(partSizes),
		Parts:     parts,
	}
}

// runSingleFileSplitCheck serves the single-file split verification path.
func runSingleFileSplitCheck(t *testing.T, local string, m split.Manifest) dto.CheckResult {
	t.Helper()
	partsDir := filepath.Base(local) + splitDirSuffix
	f := &splitVerifyFake{
		manifest: mustMarshal(t, m),
		parts: []map[string]any{
			{"Path": "part-0001.bin", "Name": "part-0001.bin", "Size": m.Parts[0].Size},
		},
		statItems: map[string]map[string]any{
			"backups/" + partsDir: {"Path": partsDir, "Name": partsDir, "IsDir": true},
		},
	}
	s := &Service{eng: &stubEngine{client: f.client(t)}}
	res := s.VerifyBackup("acct", local, "backups")
	assertSplitManifestFallback(t, f, "acct:", "backups/"+partsDir+"/manifest.json")
	return res
}

func TestVerifyBackupSplitSetDetectsChangedSource(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "movie.mp4")
	writeTestFile(t, local, "hello")

	t.Run("same size different content", func(t *testing.T) {
		m := testSplitManifest(t, local, 5)
		// "world" is the same length as "hello", so only the content differs.
		m.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("world")))
		res := runSingleFileSplitCheck(t, local, m)
		if res.Err != nil {
			t.Fatalf("VerifyBackup: %+v", res.Err)
		}
		if res.Differ != 1 || res.Same != 0 {
			t.Fatalf("result = %+v, want the changed source to differ", res)
		}
	})

	t.Run("modification time changed", func(t *testing.T) {
		m := testSplitManifest(t, local, 5)
		m.ModTime -= 60
		res := runSingleFileSplitCheck(t, local, m)
		if res.Err != nil {
			t.Fatalf("VerifyBackup: %+v", res.Err)
		}
		if res.Differ != 1 || res.Same != 0 {
			t.Fatalf("result = %+v, want the newer source to differ", res)
		}
	})
}

func TestVerifyBackupHonorsBackupExcludes(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "Photos")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(local, "keep.txt"), "keep")
	writeTestFile(t, filepath.Join(local, "skip.tmp"), "skip")
	writeTestFile(t, filepath.Join(local, "lost.txt"), "lost")

	f := &splitVerifyFake{checkReply: map[string]any{
		"missingOnDst": []string{"skip.tmp", "lost.txt"},
		"status":       "2 differences found",
		"success":      false,
	}}
	s := &Service{eng: &stubEngine{client: f.client(t)}}

	t.Run("only matching files are filtered", func(t *testing.T) {
		// No excludes: both files exist locally but not remotely.
		if res := s.verifyBackup("acct", local, "backups", nil); res.Missing != 2 {
			t.Fatalf("Missing = %d, want 2 without excludes (%+v)", res.Missing, res)
		}
		// The excluded file is expected to be absent; the genuine loss stays.
		res := s.verifyBackup("acct", local, "backups", []string{"*.tmp"})
		if res.Missing != 1 || res.Differ != 0 {
			t.Fatalf("result = %+v, want only lost.txt missing", res)
		}
		// Excluding both leaves nothing missing.
		res = s.verifyBackup("acct", local, "backups", []string{"*.tmp", "lost.txt"})
		if res.Missing != 0 || res.Differ != 0 {
			t.Fatalf("Missing = %d, want 0 when both files are excluded (%+v)", res.Missing, res)
		}
		// rclone still reports success=false because the excluded files are
		// absent remotely; that must not be presented as a failed verification.
		if res.Err != nil {
			t.Fatalf("a complete backup was reported as unverified: %+v", res.Err)
		}
		if res.Status != "success" {
			t.Fatalf("status = %q, want success", res.Status)
		}
		if res.Same != 1 {
			t.Fatalf("Same = %d, want 1 (keep.txt only)", res.Same)
		}
	})

	t.Run("excludes come from the matching backup job", func(t *testing.T) {
		s.jobs = map[string]*jobState{
			"job-1": {
				ID: "job-1", Kind: "backup", State: "done", AccountID: "acct",
				DestRoot: "backups", Sources: []string{local},
				Excludes: []string{"*.tmp"}, StartedAt: time.Now(),
			},
		}
		res := s.VerifyBackup("acct", local, "backups")
		if res.Err != nil {
			t.Fatalf("VerifyBackup: %+v", res.Err)
		}
		if res.Missing != 1 || res.Differ != 0 {
			t.Fatalf("result = %+v, want only lost.txt missing", res)
		}
		if res.Same != 1 {
			t.Fatalf("Same = %d, want 1 (keep.txt only)", res.Same)
		}
	})

	t.Run("excludes come from the journal after a restart", func(t *testing.T) {
		s.jobs = nil
		s.journalEntries = []dto.JournalEntry{{
			ID: "job-1", Kind: "backup", State: "done",
			Request: dto.PlanRequest{
				AccountID: "acct", DestRoot: "backups",
				SourcePaths: []string{local}, Excludes: []string{"*.tmp"},
			},
		}}
		res := s.VerifyBackup("acct", local, "backups")
		if res.Missing != 1 || res.Differ != 0 {
			t.Fatalf("result = %+v, want only lost.txt missing", res)
		}
	})
}

func TestVerifyBackupNewestBackupDecidesExcludes(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "Photos")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(local, "keep.txt"), "keep")
	writeTestFile(t, filepath.Join(local, "skip.tmp"), "skip")

	f := &splitVerifyFake{checkReply: map[string]any{
		"missingOnDst": []string{"skip.tmp"},
		"status":       "1 differences found",
		"success":      false,
	}}
	older := time.Now().Add(-time.Hour)

	t.Run("a newer job without excludes does not inherit older patterns", func(t *testing.T) {
		s := &Service{eng: &stubEngine{client: f.client(t)}}
		s.jobs = map[string]*jobState{
			"old": {
				ID: "old", Kind: "backup", State: "done", AccountID: "acct",
				DestRoot: "backups", Sources: []string{local},
				Excludes: []string{"*.tmp"}, StartedAt: older,
			},
			"new": {
				ID: "new", Kind: "backup", State: "done", AccountID: "acct",
				DestRoot: "backups", Sources: []string{local},
				StartedAt: time.Now(),
			},
		}

		res := s.VerifyBackup("acct", local, "backups")
		if res.Err != nil {
			t.Fatalf("VerifyBackup: %+v", res.Err)
		}
		// The newest backup copied no excludes, so skip.tmp was meant to be
		// uploaded and is genuinely missing; the older job's "*.tmp" must not
		// hide it.
		if res.Missing != 1 || res.Differ != 0 {
			t.Fatalf("result = %+v, want skip.tmp reported missing", res)
		}
		if res.Same != 1 {
			t.Fatalf("Same = %d, want 1 (keep.txt only)", res.Same)
		}
	})

	t.Run("the newest job's own patterns are used", func(t *testing.T) {
		s := &Service{eng: &stubEngine{client: f.client(t)}}
		s.jobs = map[string]*jobState{
			"old": {
				ID: "old", Kind: "backup", State: "done", AccountID: "acct",
				DestRoot: "backups", Sources: []string{local},
				Excludes: []string{"keep.txt"}, StartedAt: older,
			},
			"new": {
				ID: "new", Kind: "backup", State: "done", AccountID: "acct",
				DestRoot: "backups", Sources: []string{local},
				Excludes: []string{"*.tmp"}, StartedAt: time.Now(),
			},
		}

		res := s.VerifyBackup("acct", local, "backups")
		if res.Err != nil {
			t.Fatalf("VerifyBackup: %+v", res.Err)
		}
		if res.Missing != 0 || res.Differ != 0 || res.Same != 1 {
			t.Fatalf("result = %+v, want the newest job's excludes applied", res)
		}
	})

	t.Run("the newest journal entry decides after a restart", func(t *testing.T) {
		s := &Service{eng: &stubEngine{client: f.client(t)}}
		// Newest first, as the journal keeps them.
		s.journalEntries = []dto.JournalEntry{
			{
				ID: "new", Kind: "backup", State: "done", StartedAt: older.Add(time.Hour).Unix(),
				Request: dto.PlanRequest{
					AccountID: "acct", DestRoot: "backups", SourcePaths: []string{local},
				},
			},
			{
				ID: "old", Kind: "backup", State: "done", StartedAt: older.Unix(),
				Request: dto.PlanRequest{
					AccountID: "acct", DestRoot: "backups", SourcePaths: []string{local},
					Excludes: []string{"*.tmp"},
				},
			},
		}

		res := s.VerifyBackup("acct", local, "backups")
		if res.Err != nil {
			t.Fatalf("VerifyBackup: %+v", res.Err)
		}
		if res.Missing != 1 || res.Differ != 0 {
			t.Fatalf("result = %+v, want skip.tmp reported missing", res)
		}
	})
}

func TestVerifyBackupExcludePatternsVerifyCleanly(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "Photos")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(local, "keep.txt"), "keep")
	writeTestFile(t, filepath.Join(local, "skip.tmp"), "skip")

	f := &splitVerifyFake{checkReply: map[string]any{
		// The excluded file exists locally but was intentionally not uploaded,
		// so rclone reports it and sets success=false.
		"missingOnDst": []string{"skip.tmp"},
		"status":       "1 differences found",
		"success":      false,
	}}
	s := &Service{eng: &stubEngine{client: f.client(t)}}
	s.jobs = map[string]*jobState{
		"job": {
			ID: "job", Kind: "backup", State: "done", AccountID: "acct",
			DestRoot: "backups", Sources: []string{local},
			Excludes: []string{"*.tmp"}, StartedAt: time.Now(),
		},
	}

	t.Run("the excluded file is not a difference", func(t *testing.T) {
		res := s.VerifyBackup("acct", local, "backups")
		if res.Err != nil {
			t.Fatalf("a complete exclude-pattern backup was reported as unverified: %+v", res.Err)
		}
		if res.Missing != 0 || res.Differ != 0 {
			t.Fatalf("result = %+v, want no differences", res)
		}
		if res.Same != 1 {
			t.Fatalf("Same = %d, want 1 (keep.txt only)", res.Same)
		}
		if res.Status != "success" {
			t.Fatalf("status = %q, want success", res.Status)
		}
	})

	t.Run("a genuine missing file is still reported", func(t *testing.T) {
		writeTestFile(t, filepath.Join(local, "lost.txt"), "lost")
		f.checkReply = map[string]any{
			"missingOnDst": []string{"skip.tmp", "lost.txt"},
			"status":       "2 differences found",
			"success":      false,
		}

		res := s.VerifyBackup("acct", local, "backups")
		if res.Err != nil {
			t.Fatalf("VerifyBackup: %+v", res.Err)
		}
		if res.Missing != 1 || res.Differ != 0 {
			t.Fatalf("result = %+v, want only lost.txt missing", res)
		}
		if res.Same != 1 {
			t.Fatalf("Same = %d, want 1 (keep.txt only)", res.Same)
		}
	})
}

func TestVerifyBackupWithoutExcludesReportsExcludedFile(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "Photos")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(local, "keep.txt"), "keep")
	writeTestFile(t, filepath.Join(local, "skip.tmp"), "skip")

	f := &splitVerifyFake{checkReply: map[string]any{
		"missingOnDst": []string{"skip.tmp"},
		"status":       "1 differences found",
		"success":      false,
	}}

	s := &Service{eng: &stubEngine{client: f.client(t)}}
	res := s.VerifyBackup("acct", local, "backups")

	// No job carries excludes, so the comparison is the whole tree and a real
	// missing file is still reported.
	if res.Missing != 1 || res.Differ != 0 {
		t.Fatalf("result = %+v, want skip.tmp reported missing", res)
	}
}

func TestVerifyBackupSplitLookupFailureIsError(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "Photos")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(local, "movie.mp4"), "hello")
	writeTestFile(t, filepath.Join(local, "notes.txt"), "notes")

	f := &splitVerifyFake{
		copyStatus: http.StatusInternalServerError,
		checkReply: map[string]any{
			"missingOnSrc": []string{"movie.mp4.cloudbox-part/part-0001.bin"},
			"missingOnDst": []string{"movie.mp4"},
			"status":       "2 differences found",
			"success":      false,
		},
	}

	s := &Service{eng: &stubEngine{client: f.client(t)}}
	res := s.VerifyBackup("acct", local, "backups")

	if res.Err == nil {
		t.Fatalf("a failed manifest lookup was reported as a result: %+v", res)
	}
	if res.Differ != 0 {
		t.Fatalf("Differ = %d, want 0: a lookup failure is not a data difference", res.Differ)
	}
	if len(f.copies) != 1 {
		t.Fatalf("manifest copy calls = %d, want 1", len(f.copies))
	}
}

func TestSummarizeCheckReportsUncheckedFiles(t *testing.T) {
	s := &Service{}
	res := &rc.CheckResult{
		Status:  "1 error while checking",
		Success: false,
		Error:   []string{"docs/report.pdf: hash failed"},
	}
	out := s.summarizeCheck(nil, "acct:", t.TempDir(), "", res, nil)

	if out.Err == nil {
		t.Fatal("a check with per-file errors was reported as complete")
	}
	if out.Differ != 0 || out.Missing != 0 {
		t.Fatalf("counts = %+v, want 0 differ/missing", out)
	}
	if out.Errors != 1 {
		t.Fatalf("Errors = %d, want 1", out.Errors)
	}
	if len(out.ErrorExamples) != 1 || out.ErrorExamples[0] != "docs/report.pdf: hash failed" {
		t.Fatalf("ErrorExamples = %v", out.ErrorExamples)
	}
}

func TestSummarizeCheckFillsSameCount(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "a")
	writeTestFile(t, filepath.Join(dir, "b.txt"), "b")

	out := (&Service{}).summarizeCheck(nil, "acct:", dir, "", &rc.CheckResult{Success: true}, nil)

	if out.Err != nil {
		t.Fatalf("summarizeCheck: %+v", out.Err)
	}
	if out.Same != 2 || out.Differ != 0 || out.Missing != 0 {
		t.Fatalf("result = %+v, want Same=2", out)
	}
}

// ---------------------------------------------------------------- cloud move

type moveCall struct {
	SrcFs     string `json:"srcFs"`
	SrcRemote string `json:"srcRemote"`
	DstFs     string `json:"dstFs"`
	DstRemote string `json:"dstRemote"`
}

type remoteOpsServer struct {
	client *rc.Client
	moves  []moveCall
	stats  []string
}

// newRemoteOpsServer serves operations/stat from items (remote path -> item)
// and records operations/movefile requests.
func newRemoteOpsServer(t *testing.T, items map[string]map[string]any) *remoteOpsServer {
	t.Helper()
	s := &remoteOpsServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/operations/stat":
			var in struct {
				Remote string `json:"remote"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode stat: %v", err)
			}
			s.stats = append(s.stats, in.Remote)
			item, ok := items[in.Remote]
			if !ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"item": nil})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
		case "/operations/movefile":
			var in moveCall
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode movefile: %v", err)
			}
			s.moves = append(s.moves, in)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s.client = rc.New(srv.URL, "", "", nil)
	return s
}

func remoteTestEntry(name string, size int64, mod time.Time) map[string]any {
	return map[string]any{"Path": name, "Name": name, "Size": size, "ModTime": mod}
}

func TestRenameCloudSameNameIsNoOp(t *testing.T) {
	srv := newRemoteOpsServer(t, nil)
	s := &Service{eng: &stubEngine{client: srv.client}}

	if out := s.RenameCloud("acct", "docs/report.pdf", "report.pdf"); out.Err != nil {
		t.Fatalf("RenameCloud: %+v", out.Err)
	}
	if len(srv.stats) != 0 {
		t.Fatalf("stat calls = %v, want none for an unchanged name", srv.stats)
	}
	if len(srv.moves) != 1 {
		t.Fatalf("movefile calls = %d, want 1", len(srv.moves))
	}
	got := srv.moves[0]
	if got.SrcFs != "acct:docs" || got.SrcRemote != "report.pdf" ||
		got.DstFs != "acct:docs" || got.DstRemote != "report.pdf" {
		t.Fatalf("movefile = %+v", got)
	}
}

func TestRenameCloudAllowsCaseOnlyRenameOfSameObject(t *testing.T) {
	mod := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	item := remoteTestEntry("report.pdf", 42, mod)
	srv := newRemoteOpsServer(t, map[string]map[string]any{
		"docs/report.pdf": item,
		"docs/Report.pdf": item,
	})
	s := &Service{eng: &stubEngine{client: srv.client}}

	if out := s.RenameCloud("acct", "docs/report.pdf", "Report.pdf"); out.Err != nil {
		t.Fatalf("RenameCloud: %+v", out.Err)
	}
	if len(srv.moves) != 1 {
		t.Fatalf("movefile calls = %d, want 1", len(srv.moves))
	}
	got := srv.moves[0]
	if got.SrcRemote != "report.pdf" || got.DstRemote != "Report.pdf" {
		t.Fatalf("movefile = %+v", got)
	}
}

func TestRenameCloudRefusesDifferentObject(t *testing.T) {
	mod := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	src := remoteTestEntry("report.pdf", 42, mod)
	other := remoteTestEntry("Report.pdf", 99, mod.Add(time.Hour))
	srv := newRemoteOpsServer(t, map[string]map[string]any{
		"docs/report.pdf": src,
		"docs/Report.pdf": other,
	})
	s := &Service{eng: &stubEngine{client: srv.client}}

	out := s.RenameCloud("acct", "docs/report.pdf", "Report.pdf")
	if out.Err == nil {
		t.Fatal("RenameCloud reported success over a different object")
	}
	if len(srv.moves) != 0 {
		t.Fatalf("movefile was called %d times, want 0", len(srv.moves))
	}
}

func TestMoveCloudAllowsCaseOnlyPathChangeOfSameObject(t *testing.T) {
	mod := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	item := remoteTestEntry("report.pdf", 42, mod)
	srv := newRemoteOpsServer(t, map[string]map[string]any{
		"Docs/report.pdf": item,
		"docs/report.pdf": item,
	})
	s := &Service{eng: &stubEngine{client: srv.client}}

	out := s.MoveCloud("acct", []string{"Docs/report.pdf"}, "docs")
	if out.Err != nil || out.Done != 1 {
		t.Fatalf("MoveCloud = %+v", out)
	}
	if len(srv.moves) != 1 {
		t.Fatalf("movefile calls = %d, want 1", len(srv.moves))
	}
	got := srv.moves[0]
	if got.SrcFs != "acct:Docs" || got.DstFs != "acct:docs" {
		t.Fatalf("movefile = %+v", got)
	}
}

type copyCloudServer struct {
	client  *rc.Client
	started int
}

// newCopyCloudServer serves operations/stat from items and counts started
// copy jobs.
func newCopyCloudServer(t *testing.T, items map[string]map[string]any) *copyCloudServer {
	t.Helper()
	c := &copyCloudServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/operations/stat":
			var in struct {
				Remote string `json:"remote"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(map[string]any{"item": items[in.Remote]})
		case "/operations/copyfile", "/sync/copy":
			c.started++
			_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c.client = rc.New(srv.URL, "", "", nil)
	return c
}

func TestCopyCloudRefusesExistingDestination(t *testing.T) {
	mod := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	srv := newCopyCloudServer(t, map[string]map[string]any{
		"docs/report.pdf":    remoteTestEntry("report.pdf", 10, mod),
		"archive/report.pdf": remoteTestEntry("report.pdf", 20, mod.Add(time.Hour)),
	})
	s := &Service{
		eng: &stubEngine{client: srv.client}, store: newServiceStore(t),
		jobs: make(map[string]*jobState),
	}

	out := s.CopyCloud("acct", []string{"docs/report.pdf"}, "acct", "archive")

	if out.Err == nil {
		t.Fatal("CopyCloud reported success over an existing destination")
	}
	if out.Done != 0 || len(out.Failed) != 1 || out.Failed[0] != "docs/report.pdf" {
		t.Fatalf("CopyCloud = %+v", out)
	}
	if srv.started != 0 {
		t.Fatalf("rclone copy jobs started = %d, want 0", srv.started)
	}
}

func TestCopyCloudStartsWhenDestinationIsFree(t *testing.T) {
	mod := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	srv := newCopyCloudServer(t, map[string]map[string]any{
		"docs/report.pdf": remoteTestEntry("report.pdf", 10, mod),
	})
	s := &Service{
		eng: &stubEngine{client: srv.client}, store: newServiceStore(t),
		jobs: make(map[string]*jobState),
	}

	out := s.CopyCloud("acct", []string{"docs/report.pdf"}, "acct", "archive")

	if out.Err != nil || out.Done != 1 {
		t.Fatalf("CopyCloud = %+v", out)
	}
	if srv.started != 1 {
		t.Fatalf("rclone copy jobs started = %d, want 1", srv.started)
	}
}
