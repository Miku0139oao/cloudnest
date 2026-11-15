package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cloudnest/internal/dto"
	"cloudnest/internal/rc"
)

func TestDestRootOf(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", defaultDestRoot},
		{"   ", defaultDestRoot},
		{"/", defaultDestRoot},
		{"/backup/", "backup"},
		{" /電腦備份 ", "電腦備份"},
	}
	for _, tc := range tests {
		if got := destRootOf(tc.in); got != tc.want {
			t.Errorf("destRootOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRemoteJoin(t *testing.T) {
	tests := []struct {
		parent, child, want string
	}{
		{"backup", "Pictures", "backup/Pictures"},
		{"/backup/", "/Pictures/", "backup/Pictures"},
		{"", "Pictures", "Pictures"},
		{"backup", "", "backup"},
	}
	for _, tc := range tests {
		if got := remoteJoin(tc.parent, tc.child); got != tc.want {
			t.Errorf("remoteJoin(%q, %q) = %q, want %q", tc.parent, tc.child, got, tc.want)
		}
	}
}

func TestPlainRemoteName(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"Pictures", true},
		{"我的 相片", true},
		{"", false},
		{".", false},
		{"..", false},
		{`\`, false},
		{"/", false},
		{`a\b`, false},
		{"a/b", false},
		{"C:", false},
	}
	for _, tc := range tests {
		if got := plainRemoteName(tc.in); got != tc.want {
			t.Errorf("plainRemoteName(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestPlanMirrorMissingRemoteSubtreeSucceeds covers a first backup: the
// destination folder exists but destRoot/<source folder> does not yet, so
// rclone answers operations/list with 404. That is an empty subtree, not a
// plan failure, or mirror mode could never be used the first time.
func TestPlanMirrorMissingRemoteSubtreeSucceeds(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/list" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  "error in ListJSON: directory not found",
			"status": http.StatusNotFound,
			"path":   "operations/list",
		})
	}))
	t.Cleanup(srv.Close)

	s := &Service{
		eng:   &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)},
		store: newServiceStore(t),
	}
	res := s.Plan(dto.PlanRequest{
		SourcePaths: []string{src},
		AccountID:   "gdrive",
		DestRoot:    "backup",
		Mode:        "mirror",
	})
	if res.Err != nil {
		t.Fatalf("Plan: %+v", res.Err)
	}
	if res.Plan.DeleteCount != 0 || res.Plan.DeleteBytes != 0 {
		t.Fatalf("deletions = %d/%d, want 0/0", res.Plan.DeleteCount, res.Plan.DeleteBytes)
	}
	if res.Plan.Files != 1 {
		t.Fatalf("files = %d, want 1", res.Plan.Files)
	}
}

// TestPlanMirrorListsSourceSubtree pins the layout the mirror warning depends
// on: a folder backup lands in destRoot/<source folder>, so the preflight must
// list that subtree only and compare paths relative to it. Listing destRoot
// would count every other backup in the destination as a deletion.
func TestPlanMirrorListsSourceSubtree(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(filepath.Clean(src))
	wantRemote := "backup/" + base

	var gotFs, gotRemote string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/list" {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Fs     string `json:"fs"`
			Remote string `json:"remote"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		gotFs, gotRemote = in.Fs, in.Remote
		// rclone reports Path relative to the remote root, including the
		// listed directory.
		_ = json.NewEncoder(w).Encode(map[string]any{"list": []map[string]any{
			{"Path": wantRemote + "/keep.txt", "Name": "keep.txt", "Size": 2, "ModTime": "2026-01-02T03:04:05Z"},
			{"Path": wantRemote + "/gone.txt", "Name": "gone.txt", "Size": 7, "ModTime": "2026-01-02T03:04:05Z"},
			{"Path": wantRemote + "/sub/old.bin", "Name": "old.bin", "Size": 3, "ModTime": "2026-01-02T03:04:05Z"},
		}})
	}))
	t.Cleanup(srv.Close)

	s := &Service{
		eng:   &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)},
		store: newServiceStore(t),
	}
	res := s.Plan(dto.PlanRequest{
		SourcePaths: []string{src},
		AccountID:   "gdrive",
		DestRoot:    "backup",
		Mode:        "mirror",
	})
	if res.Err != nil {
		t.Fatalf("Plan: %+v", res.Err)
	}
	if gotFs != "gdrive:" {
		t.Errorf("list fs = %q, want gdrive:", gotFs)
	}
	if gotRemote != wantRemote {
		t.Errorf("list remote = %q, want %q", gotRemote, wantRemote)
	}
	if res.Plan.DeleteCount != 2 {
		t.Fatalf("delete count = %d (%v), want 2", res.Plan.DeleteCount, res.Plan.Deletions)
	}
	got := map[string]bool{}
	for _, d := range res.Plan.Deletions {
		got[d.Path] = true
	}
	if !got["gone.txt"] || !got["sub/old.bin"] {
		t.Fatalf("deletions = %v, want gone.txt and sub/old.bin", res.Plan.Deletions)
	}
	if got["keep.txt"] {
		t.Fatal("a file present locally was reported as a deletion")
	}
}

func TestRCListerStripsListedPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/list" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"list": []map[string]any{
			{"Path": "backup/docs/a.txt", "Name": "a.txt", "Size": 1, "ModTime": "2026-01-02T03:04:05Z"},
			{"Path": "backup/docs/sub/b.txt", "Name": "b.txt", "Size": 2, "ModTime": "2026-01-02T03:04:05Z"},
			{"Path": "backup/docs", "Name": "docs", "Size": 0, "ModTime": "2026-01-02T03:04:05Z"},
		}})
	}))
	t.Cleanup(srv.Close)
	l := &rcLister{c: rc.New(srv.URL, "", "", nil), account: "gdrive"}

	entries, err := l.ListRecursive(context.Background(), "/backup/docs/")
	if err != nil {
		t.Fatalf("ListRecursive: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d (%+v), want 2", len(entries), entries)
	}
	if entries[0].Path != "a.txt" || entries[1].Path != "sub/b.txt" {
		t.Fatalf("paths = %q, %q, want a.txt and sub/b.txt", entries[0].Path, entries[1].Path)
	}
}
