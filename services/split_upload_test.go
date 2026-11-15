package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudnest/internal/rc"
	"cloudnest/internal/settings"
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
	additive := splitOptionsFor("", 0)
	if !additive.Enabled {
		t.Fatal("additive backups must allow split uploads")
	}
	if additive.Threshold != split.DefaultOptions().Threshold {
		t.Fatalf("threshold = %d, want %d", additive.Threshold, split.DefaultOptions().Threshold)
	}
	if additive.PartSize != split.DefaultOptions().PartSize {
		t.Fatalf("part size = %d, want default %d", additive.PartSize, split.DefaultOptions().PartSize)
	}
	// The product cap keeps manifest.json far below its 64 KiB limit while
	// letting the configured part size decide the layout up to that point.
	if additive.MaxParts != splitMaxParts {
		t.Fatalf("max parts = %d, want %d", additive.MaxParts, splitMaxParts)
	}
	if mirror := splitOptionsFor("mirror", 512); mirror.Enabled {
		t.Fatal("mirror mode must disable split uploads")
	}
	// The Settings part-size picker is in MiB and must reach split.Options.
	if got, want := splitOptionsFor("", 512).PartSize, int64(512)<<20; got != want {
		t.Fatalf("part size = %d, want %d", got, want)
	}
}

func TestFilteredSyncRequest(t *testing.T) {
	req := filteredSyncRequest(`C:\src`, "acct:backup", "gui-job-1", 4, 7, []string{"*.tmp", "node_modules"})

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
	if filter["MaxSize"] != "7B" {
		t.Fatalf("MaxSize = %#v, want \"7B\" (rclone reads a bare number as KiB)", filter["MaxSize"])
	}
	excludes, ok := filter["ExcludeRule"].([]string)
	wantRules := []string{"*.tmp", "*.tmp/**", "node_modules", "node_modules/**"}
	if !ok || len(excludes) != len(wantRules) {
		t.Fatalf("ExcludeRule = %#v, want %v", filter["ExcludeRule"], wantRules)
	}
	for i, want := range wantRules {
		if excludes[i] != want {
			t.Fatalf("ExcludeRule[%d] = %q, want %q", i, excludes[i], want)
		}
	}
	if filter["IgnoreCase"] != true {
		t.Fatalf("IgnoreCase = %#v, want true so matching follows the planner", filter["IgnoreCase"])
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
	decodedFilter := decoded["_filter"].(map[string]any)
	if decodedFilter["MaxSize"] != "7B" {
		t.Fatalf("MaxSize did not survive JSON encoding: %s", raw)
	}
	encodedExcludes, ok := decodedFilter["ExcludeRule"].([]any)
	if !ok || len(encodedExcludes) != 4 || encodedExcludes[0] != "*.tmp" || encodedExcludes[1] != "*.tmp/**" {
		t.Fatalf("ExcludeRule did not survive JSON encoding: %s", raw)
	}

	noTransfers := filteredSyncRequest("a", "b", "g", 0, -1, nil)
	if _, exists := noTransfers["_config"].(map[string]any)["Transfers"]; exists {
		t.Fatal("Transfers must be omitted when it is not positive")
	}
	if _, exists := noTransfers["_filter"].(map[string]any)["ExcludeRule"]; exists {
		t.Fatal("ExcludeRule must be omitted when there are no patterns")
	}
	if noTransfers["_filter"].(map[string]any)["MaxSize"] != "0B" {
		t.Fatalf("MaxSize = %#v, want \"0B\" for a negative cap", noTransfers["_filter"].(map[string]any)["MaxSize"])
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
	plan, err := buildSplitPlan([]string{folder, singleSmall, singleLarge}, "backup", opts, nil)
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
	plan, err := buildSplitPlan([]string{folder}, "backup", opts, nil)
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

func TestBuildSplitPlanSkipsExcluded(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "photos")
	writeSizedFile(t, filepath.Join(folder, "keep.bin"), 16)
	writeSizedFile(t, filepath.Join(folder, "skip.tmp"), 20)
	writeSizedFile(t, filepath.Join(folder, "cache", "old.bin"), 30)

	opts := split.Options{Enabled: true, Threshold: 8, PartSize: 4, MinParts: 2, MaxParts: 4}
	plan, err := buildSplitPlan([]string{folder}, "backup", opts, []string{"*.tmp", "cache"})
	if err != nil {
		t.Fatalf("buildSplitPlan: %v", err)
	}
	if got := len(plan.files); got != 1 {
		t.Fatalf("files = %d, want 1: %#v", got, plan.files)
	}
	if _, ok := findSplitFile(plan.files, "keep.bin"); !ok {
		t.Fatal("keep.bin should still be split")
	}
	if _, ok := findSplitFile(plan.files, "skip.tmp"); ok {
		t.Fatal("skip.tmp matched an exclude but was selected")
	}
	if _, ok := findSplitFile(plan.files, "old.bin"); ok {
		t.Fatal("cache/old.bin is below an excluded directory but was selected")
	}
	if !plan.sources[0].copySmall {
		t.Fatal("copySmall should stay true for the remaining large file")
	}
}

func TestBuildSplitPlanDisabled(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "raw")
	writeSizedFile(t, filepath.Join(folder, "huge.bin"), 50)

	opts := split.Options{Enabled: false, Threshold: 8, PartSize: 4, MinParts: 2, MaxParts: 4}
	plan, err := buildSplitPlan([]string{folder}, "backup", opts, nil)
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

func TestSplitManifestUsesConfiguredPartSize(t *testing.T) {
	f := splitFile{
		name:    "movie.mkv",
		rel:     "clips/movie.mkv",
		size:    1 << 30,
		modTime: time.Unix(1_700_000_000, 0),
	}

	configured := splitManifest(f, split.Options{Enabled: true, PartSize: 512 << 20, MinParts: 2, MaxParts: 16})
	if configured.PartCount != 2 || configured.ChunkSize != 512<<20 {
		t.Fatalf("configured layout = %d parts of %d bytes, want 2 parts of %d",
			configured.PartCount, configured.ChunkSize, 512<<20)
	}
	if configured.Name != f.name || configured.ModTime != f.modTime.Unix() || configured.SourceRel != f.rel {
		t.Fatalf("manifest identity fields = %#v", configured)
	}

	defaults := splitManifest(f, split.DefaultOptions())
	if defaults.PartCount != 4 || defaults.ChunkSize != 256<<20 {
		t.Fatalf("default layout = %d parts of %d bytes, want 4 parts of %d",
			defaults.PartCount, defaults.ChunkSize, 256<<20)
	}
}

func TestSplitOptionsHonourPartSizeUpToLimit(t *testing.T) {
	const gib = int64(1) << 30
	opts := splitOptionsFor("", 128)
	if opts.PartSize != 128<<20 {
		t.Fatalf("part size = %d, want 128 MiB", opts.PartSize)
	}

	// 8 GiB at 128 MiB is 64 parts: inside the cap, so the user's part size is
	// used exactly.
	small := splitManifest(splitFile{name: "a.bin", size: 8 * gib, modTime: time.Unix(0, 0)}, opts)
	if small.PartCount != 64 || small.ChunkSize != 128<<20 {
		t.Fatalf("8 GiB layout = %d parts of %d bytes, want 64 parts of 128 MiB",
			small.PartCount, small.ChunkSize)
	}

	// 64 GiB at 128 MiB would need 512 parts, above the manifest-safe cap, so
	// the chunk grows instead of the count.
	big := splitManifest(splitFile{name: "b.bin", size: 64 * gib, modTime: time.Unix(0, 0)}, opts)
	if big.PartCount != splitMaxParts {
		t.Fatalf("64 GiB parts = %d, want the %d-part cap", big.PartCount, splitMaxParts)
	}
	if big.ChunkSize <= opts.PartSize {
		t.Fatalf("64 GiB chunk = %d, want it grown above the requested %d", big.ChunkSize, opts.PartSize)
	}
}

func TestSplitPartSizesMatchesHash(t *testing.T) {
	cases := []struct {
		name string
		size int64
		opts split.Options
	}{
		{"tiny", 16, split.Options{Enabled: true, PartSize: 4, MinParts: 2, MaxParts: 16}},
		{"one mib", 1 << 20, split.Options{Enabled: true, PartSize: 256 << 10, MinParts: 2, MaxParts: 16}},
		{"odd size", (2 << 20) + 3, split.Options{Enabled: true, PartSize: 300 << 10, MinParts: 3, MaxParts: 8}},
		{"five mib", 5 << 20, split.Options{Enabled: true, PartSize: 1 << 20, MinParts: 2, MaxParts: 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "src.bin")
			if err := os.WriteFile(src, make([]byte, tc.size), 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}
			count := split.PartCount(tc.size, tc.opts)
			chunk := split.ChunkSize(tc.size, count)
			m := split.Manifest{PartCount: count, ChunkSize: chunk}
			if err := split.Hash(context.Background(), src, &m, nil); err != nil {
				t.Fatalf("split.Hash: %v", err)
			}
			got := splitPartSizes(tc.size, count, chunk)
			if len(got) != len(m.Parts) {
				t.Fatalf("splitPartSizes produced %d parts, split.Hash produced %d", len(got), len(m.Parts))
			}
			for i := range got {
				if got[i] != m.Parts[i].Size {
					t.Fatalf("part %d = %d bytes, split.Hash wrote %d", i+1, got[i], m.Parts[i].Size)
				}
			}
		})
	}
}

func TestRemoteSplitComplete(t *testing.T) {
	f, _ := fourPartFixture(t)
	opts := split.Options{Enabled: true, PartSize: 4, MinParts: 2, MaxParts: 16}
	m := splitManifest(f, opts)
	sizes := splitPartSizes(f.size, m.PartCount, m.ChunkSize)
	if len(sizes) == 0 {
		t.Fatal("fixture produced no part sizes")
	}

	parts := make([]split.PartInfo, len(sizes))
	files := map[string]int64{}
	for i, size := range sizes {
		parts[i] = split.PartInfo{
			Index: i + 1, Name: fmt.Sprintf("part-%04d.bin", i+1),
			Size: size, SHA256: strings.Repeat("a", 64),
		}
		files[parts[i].Name] = size
	}
	remoteManifest := m
	remoteManifest.Format = split.Format
	remoteManifest.SHA256 = strings.Repeat("b", 64)
	remoteManifest.Parts = parts
	data, err := json.Marshal(remoteManifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	files[manifestName] = int64(len(data))

	remote := &fakeSplitUploader{files: files, manifestData: data}
	server := httptest.NewServer(remote)
	defer server.Close()
	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "running", AccountID: "acct", Group: "gui-test"}
	const remoteDir = "backup/big.bin.cloudbox-part"

	if !s.remoteSplitComplete(context.Background(), client, j, f, m, remoteDir) {
		t.Fatal("a complete remote set was not detected")
	}

	changed := f
	changed.modTime = f.modTime.Add(time.Second)
	if s.remoteSplitComplete(context.Background(), client, j, changed, m, remoteDir) {
		t.Fatal("a source with a newer modification time was treated as already backed up")
	}

	remote.mu.Lock()
	delete(remote.files, "part-0002.bin")
	remote.mu.Unlock()
	if s.remoteSplitComplete(context.Background(), client, j, f, m, remoteDir) {
		t.Fatal("a set with a missing part was treated as complete")
	}
}

func TestTransferringLabel(t *testing.T) {
	if got := transferringLabel(nil); got != "" {
		t.Fatalf("empty label = %q, want empty", got)
	}
	one := transferringLabel([]rc.Transferring{{Name: "part-0002.bin"}})
	if one != "part-0002.bin" {
		t.Fatalf("single label = %q", one)
	}
	many := transferringLabel([]rc.Transferring{
		{Name: "part-0003.bin"}, {Name: "part-0001.bin"}, {Name: "part-0002.bin"},
	})
	if many != "part-0001.bin +2" {
		t.Fatalf("multi label = %q, want %q", many, "part-0001.bin +2")
	}
}

// fakeSplitUploader records how many copyfile calls overlap and answers each
// one with an already-finished job.
type fakeSplitUploader struct {
	mu            sync.Mutex
	nextID        int64
	inflight      int
	peak          int
	uploaded      []string
	stopped       []int64
	stoppedGroups []string
	failJobs      bool
	delay         time.Duration
	files         map[string]int64 // remote directory contents for operations/list
	manifestData  []byte           // body returned by operations/getfile
}

func (f *fakeSplitUploader) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/operations/mkdir":
		_, _ = w.Write([]byte(`{}`))
	case "/operations/list":
		f.mu.Lock()
		entries := make([]map[string]any, 0, len(f.files))
		for name, size := range f.files {
			entries = append(entries, map[string]any{"Name": name, "Size": size, "IsDir": false})
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"list": entries})
	case "/operations/getfile":
		// rclone returns the file bytes in "result" and writes no local file.
		f.mu.Lock()
		data := f.manifestData
		f.mu.Unlock()
		if len(data) == 0 {
			http.Error(w, "missing manifest", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": string(data)})
	case "/job/stopgroup":
		var in struct {
			Group string `json:"group"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.stoppedGroups = append(f.stoppedGroups, in.Group)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	case "/operations/copyfile":
		var in struct {
			SrcRemote string `json:"srcRemote"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.inflight++
		if f.inflight > f.peak {
			f.peak = f.inflight
		}
		f.nextID++
		id := f.nextID
		f.mu.Unlock()

		time.Sleep(f.delay)

		f.mu.Lock()
		f.inflight--
		f.uploaded = append(f.uploaded, in.SrcRemote)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"jobid": id})
	case "/job/status":
		f.mu.Lock()
		fail := f.failJobs
		f.mu.Unlock()
		if fail {
			_, _ = w.Write([]byte(`{"finished":true,"success":false,"error":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{"finished":true,"success":true}`))
	case "/job/stop":
		var in struct {
			JobID int64 `json:"jobid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.stopped = append(f.stopped, in.JobID)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

func TestUploadSplitPartsUploadsConcurrently(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	data := make([]byte, 16)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	parts := make([]split.PartInfo, 4)
	for i := range parts {
		sum := sha256.Sum256(data[i*4 : (i+1)*4])
		parts[i] = split.PartInfo{
			Index:  i + 1,
			Name:   fmt.Sprintf("part-%04d.bin", i+1),
			Size:   4,
			SHA256: hex.EncodeToString(sum[:]),
		}
	}
	m := split.Manifest{
		Format: split.Format, Name: "big.bin", Size: 16,
		ChunkSize: 4, PartCount: len(parts), Parts: parts,
	}
	f := splitFile{path: src, name: "big.bin", size: 16, destDir: "backup", partsDir: "big.bin.cloudbox-part"}

	remote := &fakeSplitUploader{delay: 60 * time.Millisecond}
	server := httptest.NewServer(remote)
	defer server.Close()

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "running", AccountID: "acct", Group: "gui-test"}

	stage := t.TempDir()
	if err := s.uploadSplitParts(context.Background(), client, j, f, stage, "backup/big.bin.cloudbox-part", m, 2, 1); err != nil {
		t.Fatalf("uploadSplitParts: %v", err)
	}

	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.uploaded) != len(parts) {
		t.Fatalf("uploaded %d parts, want %d", len(remote.uploaded), len(parts))
	}
	if remote.peak != 2 {
		t.Fatalf("peak concurrent uploads = %d, want 2 (the worker count)", remote.peak)
	}
	for _, p := range parts {
		if _, err := os.Stat(filepath.Join(stage, p.Name)); err != nil {
			t.Fatalf("part %s was not staged: %v", p.Name, err)
		}
	}
}

// fourPartFixture returns a 16-byte source file split into four 4-byte parts
// with valid per-part hashes.
func fourPartFixture(t *testing.T) (splitFile, split.Manifest) {
	t.Helper()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "big.bin")
	data := make([]byte, 16)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	parts := make([]split.PartInfo, 4)
	for i := range parts {
		sum := sha256.Sum256(data[i*4 : (i+1)*4])
		parts[i] = split.PartInfo{
			Index:  i + 1,
			Name:   fmt.Sprintf("part-%04d.bin", i+1),
			Size:   4,
			SHA256: hex.EncodeToString(sum[:]),
		}
	}
	m := split.Manifest{
		Format: split.Format, Name: "big.bin", Size: 16,
		ChunkSize: 4, PartCount: len(parts), Parts: parts,
	}
	f := splitFile{
		path: src, name: "big.bin", size: 16,
		modTime: time.Unix(1_700_000_000, 0),
		destDir: "backup", partsDir: "big.bin.cloudbox-part",
	}
	return f, m
}

func TestUploadSplitPartsStopsWhenPaused(t *testing.T) {
	f, m := fourPartFixture(t)
	remote := &fakeSplitUploader{}
	server := httptest.NewServer(remote)
	defer server.Close()

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "paused", AccountID: "acct", Group: "gui-test"}

	err := s.uploadSplitParts(context.Background(), client, j, f, t.TempDir(), "backup/big.bin.cloudbox-part", m, 2, 1)
	if !errors.Is(err, errSplitStopped) {
		t.Fatalf("err = %v, want errSplitStopped", err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.uploaded) != 0 {
		t.Fatalf("started %d part uploads after the job was paused", len(remote.uploaded))
	}
}

func TestCopySplitPartStopsFailedJob(t *testing.T) {
	f, m := fourPartFixture(t)
	remote := &fakeSplitUploader{failJobs: true}
	server := httptest.NewServer(remote)
	defer server.Close()

	stage := t.TempDir()
	if err := split.Stage(context.Background(), f.path, m, []int{1}, stage, nil); err != nil {
		t.Fatalf("stage part: %v", err)
	}

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "running", AccountID: "acct", Group: "gui-test"}

	err := s.copySplitPart(context.Background(), client, j, stage, "backup/big.bin.cloudbox-part", m.Parts[0], 1)
	if err == nil {
		t.Fatal("copySplitPart succeeded for a failing job")
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.stopped) != 1 {
		t.Fatalf("job/stop calls = %v, want exactly the failed job to be stopped", remote.stopped)
	}
}

func TestSplitStopWatcherCancelsOnPause(t *testing.T) {
	s := &Service{jobs: map[string]*jobState{}}

	pausedCtx, pausedCancel := context.WithCancel(context.Background())
	defer pausedCancel()
	s.splitStopWatcher(&jobState{State: "paused"}, pausedCancel)(1 << 20)
	select {
	case <-pausedCtx.Done():
	default:
		t.Fatal("watcher did not cancel a paused job")
	}

	runningCtx, runningCancel := context.WithCancel(context.Background())
	defer runningCancel()
	s.splitStopWatcher(&jobState{State: "running"}, runningCancel)(1 << 20)
	select {
	case <-runningCtx.Done():
		t.Fatal("watcher cancelled a running job")
	default:
	}
}

func TestUploadSplitPartsClampsWorkersToPartCount(t *testing.T) {
	f, m := fourPartFixture(t)
	remote := &fakeSplitUploader{delay: 40 * time.Millisecond}
	server := httptest.NewServer(remote)
	defer server.Close()

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "running", AccountID: "acct", Group: "gui-test"}

	// Asking for more workers than there are parts must not start more
	// concurrent uploads than the file has parts.
	if err := s.uploadSplitParts(context.Background(), client, j, f, t.TempDir(), "backup/big.bin.cloudbox-part", m, 16, 1); err != nil {
		t.Fatalf("uploadSplitParts: %v", err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.peak > len(m.Parts) {
		t.Fatalf("peak concurrent uploads = %d, want at most %d", remote.peak, len(m.Parts))
	}
}

func TestUploadSplitPartsReportsMkdirFailure(t *testing.T) {
	f, m := fourPartFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such parent", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "running", AccountID: "acct", Group: "gui-test"}

	err := s.uploadSplitParts(context.Background(), client, j, f, t.TempDir(), "backup/big.bin.cloudbox-part", m, 2, 1)
	if err == nil || !strings.Contains(err.Error(), "creating remote parts directory") {
		t.Fatalf("err = %v, want a remote parts directory error", err)
	}
}

func TestWatchSplitSideJobsWaitsForSmallFileJob(t *testing.T) {
	remote := &fakeSplitUploader{}
	server := httptest.NewServer(remote)
	defer server.Close()

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "running", AccountID: "acct", Group: "gui-test",
		JobIDs: []int64{pendingSplitJobID, 42}}

	n, results := s.watchSplitSideJobs(context.Background(), client, j)
	if n != 1 {
		t.Fatalf("side jobs watched = %d, want 1 (the pending marker is not a job)", n)
	}
	if err := s.awaitSplitSideJobs(j, n, results); err != nil {
		t.Fatalf("awaitSplitSideJobs: %v", err)
	}
}

func TestAwaitSplitSideJobsFailsWhenSmallFileSyncFails(t *testing.T) {
	remote := &fakeSplitUploader{failJobs: true}
	server := httptest.NewServer(remote)
	defer server.Close()

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "running", AccountID: "acct", Group: "gui-test", JobIDs: []int64{7}}

	n, results := s.watchSplitSideJobs(context.Background(), client, j)
	err := s.awaitSplitSideJobs(j, n, results)
	if err == nil || !strings.Contains(err.Error(), "waiting for the small-file transfer") {
		t.Fatalf("err = %v, want a small-file transfer failure", err)
	}
}

func TestStopSplitSideJobsStopsGroup(t *testing.T) {
	remote := &fakeSplitUploader{}
	server := httptest.NewServer(remote)
	defer server.Close()

	client := rc.New(server.URL, "", "", nil)
	s := &Service{jobs: map[string]*jobState{}}
	j := &jobState{State: "failed", Group: "gui-test", JobIDs: []int64{9}}

	s.stopSplitSideJobs(client, j)
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.stoppedGroups) != 1 || remote.stoppedGroups[0] != "gui-test" {
		t.Fatalf("stopped groups = %v, want the backup group", remote.stoppedGroups)
	}
}

func TestRunSplitBackupClearsSplitRunning(t *testing.T) {
	store, err := settings.New(t.TempDir())
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}
	for _, state := range []string{"running", "paused"} {
		t.Run(state, func(t *testing.T) {
			s := &Service{jobs: map[string]*jobState{}, store: store}
			j := &jobState{
				ID: "job-" + state, Kind: "backup", State: state,
				AccountID: "acct", Group: "gui-test", SplitRunning: true,
				StartedAt: time.Now(),
			}
			plan := &splitBackupPlan{opts: split.DefaultOptions()}
			s.runSplitBackup(nil, j, plan)

			s.mu.Lock()
			running := j.SplitRunning
			gotState := j.State
			s.mu.Unlock()
			if running {
				t.Fatal("SplitRunning leaked after runSplitBackup returned; the scheduler would stay blocked")
			}
			if state == "paused" && gotState != "paused" {
				t.Fatalf("state = %q, want the pause to stand", gotState)
			}
		})
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
