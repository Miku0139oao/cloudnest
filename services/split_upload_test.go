package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"cloudnest/internal/rc"
	"cloudnest/internal/split"
)

func TestSplitStagingSpace(t *testing.T) {
	const (
		mib = int64(1 << 20)
		gib = int64(1 << 30)
	)
	tests := []struct {
		name string
		size int64
		want int64
	}{
		{name: "zero", size: 0, want: 256 * mib},
		{name: "negative treated as zero", size: -5, want: 256 * mib},
		{name: "tiny", size: 100, want: 120 + 256*mib},
		{name: "one gib", size: 1 * gib, want: 1556925645},
		{name: "four gib cap", size: 4 * gib, want: 5422396212},
		{name: "above cap uses four gib", size: 10 * gib, want: 5422396212},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitStagingSpace(tc.size); got != tc.want {
				t.Fatalf("splitStagingSpace(%d) = %d, want %d", tc.size, got, tc.want)
			}
		})
	}
}

func TestSplitStagingDirNames(t *testing.T) {
	root := filepath.Join("C:", "Cloudnest", "data", "staging")
	if got, want := splitJobStagingDir(root, "job-42"), filepath.Join(root, "split-job-42"); got != want {
		t.Fatalf("splitJobStagingDir = %q, want %q", got, want)
	}
	want := filepath.Join(root, "split-job-42", "3")
	if got := splitFileStagingDir(root, "job-42", 3); got != want {
		t.Fatalf("splitFileStagingDir = %q, want %q", got, want)
	}
}

func TestSplitOptionsFor(t *testing.T) {
	additive := splitOptionsFor("")
	if !additive.Enabled {
		t.Fatal("additive backups must allow split uploads")
	}
	if additive.Threshold != split.DefaultOptions().Threshold {
		t.Fatalf("threshold = %d, want %d", additive.Threshold, split.DefaultOptions().Threshold)
	}
	if mirror := splitOptionsFor("mirror"); mirror.Enabled {
		t.Fatal("mirror mode must disable split uploads")
	}
}

func TestFilteredSyncRequest(t *testing.T) {
	req := filteredSyncRequest(`C:\src`, "acct:backup", "gui-job-1", 4, 7)

	if req["srcFs"] != `C:\src` || req["dstFs"] != "acct:backup" {
		t.Fatalf("unexpected fs fields: %#v", req)
	}
	if req["_async"] != true || req["_group"] != "gui-job-1" {
		t.Fatalf("unexpected async control fields: %#v", req)
	}

	filter, ok := req["_filter"].(map[string]any)
	if !ok {
		t.Fatalf("_filter = %#v, want map", req["_filter"])
	}
	if filter["MaxSize"] != "7" {
		t.Fatalf("MaxSize = %#v, want \"7\"", filter["MaxSize"])
	}

	cfg, ok := req["_config"].(map[string]any)
	if !ok {
		t.Fatalf("_config = %#v, want map", req["_config"])
	}
	if cfg["Transfers"] != 4 {
		t.Fatalf("Transfers = %#v, want 4", cfg["Transfers"])
	}
	if cfg["DryRun"] != false {
		t.Fatalf("DryRun = %#v, want false", cfg["DryRun"])
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if decoded["_filter"].(map[string]any)["MaxSize"] != "7" {
		t.Fatalf("MaxSize did not survive JSON encoding: %s", raw)
	}

	noTransfers := filteredSyncRequest("a", "b", "g", 0, 0)
	if _, exists := noTransfers["_config"].(map[string]any)["Transfers"]; exists {
		t.Fatal("Transfers must be omitted when it is not positive")
	}
}

func TestBuildSplitPlan(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "photos")
	writeSizedFile(t, filepath.Join(folder, "small.txt"), 4)
	writeSizedFile(t, filepath.Join(folder, "big.bin"), 16)
	writeSizedFile(t, filepath.Join(folder, "nested", "deep.bin"), 20)

	singleSmall := filepath.Join(root, "note.txt")
	writeSizedFile(t, singleSmall, 3)
	singleLarge := filepath.Join(root, "movie.mkv")
	writeSizedFile(t, singleLarge, 30)

	opts := split.Options{Enabled: true, Threshold: 8, PartSize: 4, MinParts: 2, MaxParts: 4}
	plan, err := buildSplitPlan([]string{folder, singleSmall, singleLarge}, "backup", opts)
	if err != nil {
		t.Fatalf("buildSplitPlan: %v", err)
	}

	if len(plan.sources) != 3 {
		t.Fatalf("sources = %d, want 3", len(plan.sources))
	}
	if !plan.sources[0].isDir || !plan.sources[0].copySmall {
		t.Fatalf("folder source = %#v, want dir with small-file sync", plan.sources[0])
	}
	if got := len(plan.sources[0].large); got != 2 {
		t.Fatalf("folder large files = %d, want 2", got)
	}
	if got := len(plan.sources[1].large); got != 0 {
		t.Fatalf("small file source large = %d, want 0", got)
	}
	if got := len(plan.sources[2].large); got != 1 {
		t.Fatalf("large file source large = %d, want 1", got)
	}
	if got := len(plan.files); got != 3 {
		t.Fatalf("flat files = %d, want 3", got)
	}
	if got, want := plan.totalBytes(), int64(16+20+30); got != want {
		t.Fatalf("totalBytes = %d, want %d", got, want)
	}

	big, ok := findSplitFile(plan.files, "big.bin")
	if !ok {
		t.Fatal("big.bin was not selected")
	}
	if big.destDir != "backup/photos" || big.rel != "big.bin" || big.partsDir != "big.bin.cloudbox-part" {
		t.Fatalf("big.bin = %#v", big)
	}

	deep, ok := findSplitFile(plan.files, "deep.bin")
	if !ok {
		t.Fatal("deep.bin was not selected")
	}
	if deep.destDir != "backup/photos/nested" || deep.rel != "nested/deep.bin" {
		t.Fatalf("deep.bin = %#v", deep)
	}

	movie, ok := findSplitFile(plan.files, "movie.mkv")
	if !ok {
		t.Fatal("movie.mkv was not selected")
	}
	if movie.destDir != "backup" || movie.rel != "" || movie.partsDir != "movie.mkv.cloudbox-part" {
		t.Fatalf("movie.mkv = %#v", movie)
	}
}

func TestBuildSplitPlanWithoutLargeFiles(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "docs")
	writeSizedFile(t, filepath.Join(folder, "a.txt"), 3)
	writeSizedFile(t, filepath.Join(folder, "b.txt"), 5)

	opts := split.Options{Enabled: true, Threshold: 100, PartSize: 10, MinParts: 2, MaxParts: 4}
	plan, err := buildSplitPlan([]string{folder}, "backup", opts)
	if err != nil {
		t.Fatalf("buildSplitPlan: %v", err)
	}
	if len(plan.files) != 0 {
		t.Fatalf("files = %d, want 0", len(plan.files))
	}
	if plan.sources[0].copySmall {
		t.Fatal("copySmall must stay false when there is nothing to split")
	}
}

func TestBuildSplitPlanDisabled(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "raw")
	writeSizedFile(t, filepath.Join(folder, "huge.bin"), 50)

	opts := split.Options{Enabled: false, Threshold: 8, PartSize: 4, MinParts: 2, MaxParts: 4}
	plan, err := buildSplitPlan([]string{folder}, "backup", opts)
	if err != nil {
		t.Fatalf("buildSplitPlan: %v", err)
	}
	if len(plan.files) != 0 {
		t.Fatalf("files = %d, want 0 when split is disabled", len(plan.files))
	}
}

func TestSplitSourceChanged(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"changed", errors.New("split: source changed while hashing"), true},
		{"mismatch", errors.New("split: SHA-256 mismatch for part 1"), true},
		{"short read", fmt.Errorf("split: read source: %w", io.ErrUnexpectedEOF), true},
		{"unrelated", errors.New("split: open source: permission denied"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitSourceChanged(tc.err); got != tc.want {
				t.Fatalf("splitSourceChanged(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// fakeSplitRemote implements the two RC calls verifySplitParts makes.
type fakeSplitRemote struct {
	mu     sync.Mutex
	sizes  map[string]int64 // objects reported by operations/list
	fixed  map[string]int64 // srcRemote -> size installed by operations/copyfile
	lists  int
	copies int
}

func (f *fakeSplitRemote) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/operations/list":
		f.mu.Lock()
		f.lists++
		entries := make([]map[string]any, 0, len(f.sizes))
		for name, size := range f.sizes {
			entries = append(entries, map[string]any{"Name": name, "Size": size, "IsDir": false})
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"list": entries})
	case "/operations/copyfile":
		var in struct {
			SrcRemote string `json:"srcRemote"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.copies++
		if size, ok := f.fixed[in.SrcRemote]; ok {
			f.sizes[in.SrcRemote] = size
		}
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

func TestVerifySplitPartsRetriesBadParts(t *testing.T) {
	remote := &fakeSplitRemote{
		sizes: map[string]int64{"part-0001.bin": 4},
		fixed: map[string]int64{"part-0002.bin": 4},
	}
	server := httptest.NewServer(remote)
	defer server.Close()

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "running", AccountID: "acct", Group: "gui-test"}
	m := split.Manifest{Parts: []split.PartInfo{
		{Index: 1, Name: "part-0001.bin", Size: 4},
		{Index: 2, Name: "part-0002.bin", Size: 4},
	}}

	if err := s.verifySplitParts(context.Background(), client, j, t.TempDir(), "dest/parts", m); err != nil {
		t.Fatalf("verifySplitParts: %v", err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.copies != 1 {
		t.Fatalf("copyfile calls = %d, want 1", remote.copies)
	}
	if remote.lists != 2 {
		t.Fatalf("list calls = %d, want 2", remote.lists)
	}
}

func TestVerifySplitPartsGivesUpAfterRetries(t *testing.T) {
	remote := &fakeSplitRemote{sizes: map[string]int64{}, fixed: map[string]int64{}}
	server := httptest.NewServer(remote)
	defer server.Close()

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "running", AccountID: "acct", Group: "gui-test"}
	m := split.Manifest{Parts: []split.PartInfo{
		{Index: 1, Name: "part-0001.bin", Size: 4},
	}}

	err := s.verifySplitParts(context.Background(), client, j, t.TempDir(), "dest/parts", m)
	if err == nil {
		t.Fatal("verifySplitParts succeeded with a missing part")
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.copies != splitVerifyRetries {
		t.Fatalf("copyfile calls = %d, want %d", remote.copies, splitVerifyRetries)
	}
	if remote.lists != splitVerifyRetries+1 {
		t.Fatalf("list calls = %d, want %d", remote.lists, splitVerifyRetries+1)
	}
}

func writeSizedFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func findSplitFile(files []splitFile, name string) (splitFile, bool) {
	for _, f := range files {
		if f.name == name {
			return f, true
		}
	}
	return splitFile{}, false
}
