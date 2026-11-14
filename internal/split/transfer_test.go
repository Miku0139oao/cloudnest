package split

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const transferTestSize = int64(2*(1<<20) + 12345)

type fixture struct {
	root     string
	srcPath  string
	partsDir string
	data     []byte
	m        Manifest
}

func newHashedFixture(t *testing.T, size int64, name string) *fixture {
	t.Helper()
	root := t.TempDir()
	srcPath := filepath.Join(root, "source", name)
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	if _, err := rand.New(rand.NewSource(42)).Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	o := Options{Enabled: true, Threshold: 1, PartSize: 1 << 20, MinParts: 2, MaxParts: 8}
	count := PartCount(size, o)
	m := Manifest{
		Format:    Format,
		Name:      name,
		ModTime:   time.Now().Unix(),
		ChunkSize: ChunkSize(size, count),
		PartCount: count,
		CreatedBy: "test",
		CreatedAt: time.Now().Unix(),
		SourceRel: name,
	}
	if err := Hash(context.Background(), srcPath, &m, nil); err != nil {
		t.Fatalf("Hash: %v", err)
	}
	partsDirName := PartsDirName(name)
	if partsDirName == "" {
		t.Fatalf("PartsDirName(%q) returned fallback", name)
	}
	return &fixture{
		root:     root,
		srcPath:  srcPath,
		partsDir: filepath.Join(root, "remote", partsDirName),
		data:     data,
		m:        m,
	}
}

func (f *fixture) stageAll(t *testing.T) {
	t.Helper()
	indexes := make([]int, 0, f.m.PartCount)
	for i := 1; i <= f.m.PartCount; i++ {
		indexes = append(indexes, i)
	}
	if err := Stage(context.Background(), f.srcPath, f.m, indexes, f.partsDir, nil); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := WriteMeta(f.partsDir, f.m, Readme(f.m)); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
}

func TestSplitVerifyMergeRoundTrip(t *testing.T) {
	const name = "旅遊 報告 2026.bin"
	f := newHashedFixture(t, transferTestSize, name)

	if f.m.PartCount < 2 {
		t.Fatalf("test file produced only %d part(s)", f.m.PartCount)
	}
	if len(f.m.Parts) != f.m.PartCount {
		t.Fatalf("parts len = %d, partCount = %d", len(f.m.Parts), f.m.PartCount)
	}
	for _, p := range f.m.Parts {
		if len(p.SHA256) != 64 || p.Size <= 0 {
			t.Fatalf("bad part info: %+v", p)
		}
	}

	// Stage only two parts first, then the rest. This exercises selective
	// staging without re-reading the source for each part.
	var stagedBytes int64
	stageIdx := []int{1, 2}
	if err := Stage(context.Background(), f.srcPath, f.m, stageIdx, f.partsDir, func(n int64) {
		stagedBytes += n
	}); err != nil {
		t.Fatalf("selective Stage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.partsDir, f.m.Parts[2].Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part 3 should not exist after selective staging: %v", err)
	}
	for i := 3; i <= f.m.PartCount; i++ {
		if err := Stage(context.Background(), f.srcPath, f.m, []int{i}, f.partsDir, func(n int64) {
			stagedBytes += n
		}); err != nil {
			t.Fatalf("Stage part %d: %v", i, err)
		}
	}
	if stagedBytes != f.m.Size {
		t.Fatalf("staged bytes = %d, want %d", stagedBytes, f.m.Size)
	}

	if err := WriteMeta(f.partsDir, f.m, Readme(f.m)); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	readBack, err := ReadManifestFile(f.partsDir)
	if err != nil {
		t.Fatalf("ReadManifestFile: %v", err)
	}
	if readBack.SHA256 != f.m.SHA256 || readBack.PartCount != f.m.PartCount || readBack.Size != f.m.Size {
		t.Fatalf("manifest round trip mismatch:\n got %+v\nwant %+v", readBack, f.m)
	}

	bad, err := VerifyDir(context.Background(), f.partsDir, f.m, nil)
	if err != nil {
		t.Fatalf("VerifyDir: %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("VerifyDir bad parts = %v", bad)
	}

	info, err := InspectDir(f.partsDir)
	if err != nil {
		t.Fatalf("InspectDir: %v", err)
	}
	if info.State != SetComplete || info.PartsFound != f.m.PartCount || len(info.Missing) != 0 {
		t.Fatalf("InspectDir = %+v, want complete with all parts", info)
	}

	readme, err := os.ReadFile(filepath.Join(f.partsDir, "README.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"copy /b", "cat ", name, f.m.SHA256, "manifest.json"} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("README missing %q", want)
		}
	}

	restoreDir := filepath.Join(f.root, "restore")
	if err := os.MkdirAll(restoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(restoreDir, name)
	var mergedBytes int64
	sha, err := Merge(context.Background(), f.partsDir, dst, f.m, func(n int64) {
		mergedBytes += n
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !strings.EqualFold(sha, f.m.SHA256) {
		t.Fatalf("Merge sha = %q, want %q", sha, f.m.SHA256)
	}
	if mergedBytes != f.m.Size {
		t.Fatalf("merged bytes = %d, want %d", mergedBytes, f.m.Size)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, f.data) {
		t.Fatal("restored bytes differ from source")
	}
	if _, err := os.Stat(dst + ".cloudbox-partial"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file remained after successful merge: %v", err)
	}
}

func TestCorruptPartFailsVerifyAndMerge(t *testing.T) {
	f := newHashedFixture(t, transferTestSize, "corrupt-test.bin")
	f.stageAll(t)

	part := f.m.Parts[1]
	path := filepath.Join(f.partsDir, part.Name)
	fh, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	one := []byte{0}
	if _, err := fh.ReadAt(one, 0); err != nil {
		t.Fatal(err)
	}
	one[0] ^= 0xff
	if _, err := fh.WriteAt(one, 0); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}

	bad, err := VerifyDir(context.Background(), f.partsDir, f.m, nil)
	if err != nil {
		t.Fatalf("VerifyDir: %v", err)
	}
	if len(bad) != 1 || bad[0] != part.Index {
		t.Fatalf("VerifyDir bad = %v, want [%d]", bad, part.Index)
	}

	dst := filepath.Join(t.TempDir(), "restored.bin")
	sha, err := Merge(context.Background(), f.partsDir, dst, f.m, nil)
	if err == nil {
		t.Fatal("Merge unexpectedly succeeded with corrupt part")
	}
	if sha != "" {
		t.Fatalf("Merge returned sha %q on failure", sha)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination exists after failed Merge: %v", statErr)
	}
	if _, statErr := os.Stat(dst + ".cloudbox-partial"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial exists after failed Merge: %v", statErr)
	}
}

func TestMissingPartInspectionAndMerge(t *testing.T) {
	f := newHashedFixture(t, transferTestSize, "missing-test.bin")
	f.stageAll(t)

	part := f.m.Parts[1]
	if err := os.Remove(filepath.Join(f.partsDir, part.Name)); err != nil {
		t.Fatal(err)
	}

	info, err := InspectDir(f.partsDir)
	if err != nil {
		t.Fatalf("InspectDir: %v", err)
	}
	if info.State != SetCorrupt {
		t.Fatalf("state = %q, want corrupt", info.State)
	}
	if len(info.Missing) != 1 || info.Missing[0] != part.Index {
		t.Fatalf("missing = %v, want [%d]", info.Missing, part.Index)
	}
	if info.PartsFound != f.m.PartCount-1 {
		t.Fatalf("partsFound = %d, want %d", info.PartsFound, f.m.PartCount-1)
	}

	dst := filepath.Join(t.TempDir(), "restored.bin")
	if _, err := Merge(context.Background(), f.partsDir, dst, f.m, nil); err == nil {
		t.Fatal("Merge unexpectedly succeeded with a missing part")
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after failed Merge: %v", err)
	}
}

func TestMissingManifestIsIncomplete(t *testing.T) {
	f := newHashedFixture(t, transferTestSize, "manifest-test.bin")
	f.stageAll(t)
	if err := os.Remove(filepath.Join(f.partsDir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	info, err := InspectDir(f.partsDir)
	if err != nil {
		t.Fatalf("InspectDir: %v", err)
	}
	if info.State != SetIncomplete {
		t.Fatalf("state = %q, want incomplete", info.State)
	}
	if info.PartsFound != f.m.PartCount {
		t.Fatalf("partsFound = %d, want %d", info.PartsFound, f.m.PartCount)
	}
}

func TestCanceledOperations(t *testing.T) {
	f := newHashedFixture(t, transferTestSize, "cancel.bin")
	f.stageAll(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := VerifyDir(ctx, f.partsDir, f.m, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("VerifyDir error = %v, want context.Canceled", err)
	}

	dst := filepath.Join(t.TempDir(), "canceled.bin")
	if _, err := Merge(ctx, f.partsDir, dst, f.m, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Merge error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dst + ".cloudbox-partial"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial remains after canceled Merge: %v", err)
	}

	root := t.TempDir()
	src := filepath.Join(root, "src.bin")
	if err := os.WriteFile(src, bytes.Repeat([]byte("x"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Manifest{PartCount: 2, ChunkSize: 1 << 20}
	if err := Hash(ctx, src, &m, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Hash error = %v, want context.Canceled", err)
	}
}

func TestTinyHashStageMerge(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "tiny.bin")
	content := []byte("hello")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	m := Manifest{
		Format:    Format,
		Name:      "tiny.bin",
		PartCount: 3,
		ChunkSize: 1 << 20,
	}
	if err := Hash(context.Background(), src, &m, nil); err != nil {
		t.Fatalf("Hash: %v", err)
	}
	var total int64
	for _, p := range m.Parts {
		if p.Size <= 0 {
			t.Fatalf("non-positive part size: %+v", p)
		}
		total += p.Size
	}
	if total != int64(len(content)) || len(m.Parts) != 3 {
		t.Fatalf("unexpected parts: total=%d parts=%+v", total, m.Parts)
	}

	partsDir := filepath.Join(root, "parts")
	if err := Stage(context.Background(), src, m, []int{1, 2, 3}, partsDir, nil); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	bad, err := VerifyDir(context.Background(), partsDir, m, nil)
	if err != nil || len(bad) != 0 {
		t.Fatalf("VerifyDir: bad=%v err=%v", bad, err)
	}
	dst := filepath.Join(root, "merged.bin")
	if _, err := Merge(context.Background(), partsDir, dst, m, nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("merged content = %q, want %q", got, content)
	}
}
