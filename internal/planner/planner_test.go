package planner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cloudnest/internal/dto"
)

func fakeWalk(files ...LocalFile) LocalWalkFunc {
	return func(ctx context.Context, root string, fn func(LocalFile) error) error {
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(f); err != nil {
				return err
			}
		}
		return nil
	}
}

type fakeRemote struct {
	entries []dto.Entry
	err     error
	called  bool
}

func (f *fakeRemote) ListRecursive(ctx context.Context, remotePath string) ([]dto.Entry, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	return append([]dto.Entry(nil), f.entries...), nil
}

func testRequest(mode string) Request {
	return Request{
		LocalRoot:   `C:\source`,
		RemotePath:  "drive:backup",
		Mode:        mode,
		TokenSecret: []byte("test-secret"),
		TokenTTL:    time.Hour,
	}
}

func TestPlanAdditiveCountsFilesAndBytes(t *testing.T) {
	remote := &fakeRemote{err: errors.New("remote must not be listed")}
	req := testRequest(ModeAdditive)
	req.Excludes = []string{"*.tmp"}

	res, err := Plan(context.Background(), req, fakeWalk(
		LocalFile{Path: "a.txt", Size: 10},
		LocalFile{Path: `sub\b.bin`, Size: 20},
		LocalFile{Path: "sub/skip.TMP", Size: 30},
	), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if remote.called {
		t.Fatal("additive mode must not list the remote")
	}
	if res.Files != 2 || res.Bytes != 30 {
		t.Fatalf("Files/Bytes = %d/%d, want 2/30", res.Files, res.Bytes)
	}
	if res.DeleteCount != 0 || res.DeleteBytes != 0 || len(res.Deletions) != 0 {
		t.Fatalf("additive plan has deletions: %+v", res)
	}
	if res.Estimated || len(res.Warnings) != 0 {
		t.Fatalf("additive plan unexpectedly estimated: %+v", res)
	}
	if res.Token == "" {
		t.Fatal("plan token is empty")
	}
}

func TestPlanMirrorDetectsDeletionsAndAliases(t *testing.T) {
	req := testRequest(ModeMirror)
	remote := &fakeRemote{entries: []dto.Entry{
		{Path: "Docs/README.TXT", Size: 5}, // alias of local Docs/Readme.txt
		{Path: "/a.bin", Size: 7},          // leading-slash alias of local a.bin
		{Path: "old/file.txt", Size: 11},   // deletion
		{Path: "Zeta.txt", Size: 13},       // deletion
		{Path: "OLD/FILE.TXT", Size: 11},   // duplicate alias of old/file.txt
		{Path: "somedir", IsDir: true},     // directories are never deletions
		{Path: "/", Size: 1},               // normalises to empty
	}}

	res, err := Plan(context.Background(), req, fakeWalk(
		LocalFile{Path: "Docs/Readme.txt", Size: 5},
		LocalFile{Path: "a.bin", Size: 7},
	), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !remote.called {
		t.Fatal("mirror mode should list the remote")
	}
	if res.DeleteCount != 2 || res.DeleteBytes != 24 {
		t.Fatalf("DeleteCount/DeleteBytes = %d/%d, want 2/24", res.DeleteCount, res.DeleteBytes)
	}
	if len(res.Deletions) != 2 {
		t.Fatalf("len(Deletions) = %d, want 2: %+v", len(res.Deletions), res.Deletions)
	}
	want := []string{"old/file.txt", "Zeta.txt"}
	for i, w := range want {
		if res.Deletions[i].Path != w {
			t.Fatalf("Deletions[%d] = %q, want %q (%+v)", i, res.Deletions[i].Path, w, res.Deletions)
		}
	}
	if res.Estimated {
		t.Fatal("small mirror plan should not be estimated")
	}
}

func TestPlanExcludeGlobs(t *testing.T) {
	req := testRequest(ModeAdditive)
	req.Excludes = []string{"*.TMP", "node_modules", "/root-only.txt"}

	res, err := Plan(context.Background(), req, fakeWalk(
		LocalFile{Path: "keep.txt", Size: 2},
		LocalFile{Path: "drop.tmp", Size: 1},
		LocalFile{Path: "sub/DROP.TmP", Size: 3},
		LocalFile{Path: "sub/deep/keep2.txt", Size: 4},
		LocalFile{Path: "node_modules/pkg/index.js", Size: 5},
		LocalFile{Path: "root-only.txt", Size: 6},
	), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Files != 2 || res.Bytes != 6 {
		t.Fatalf("Files/Bytes = %d/%d, want 2/6", res.Files, res.Bytes)
	}
}

func TestPlanMaxEntriesCapsListButKeepsCounts(t *testing.T) {
	const deletions = 250
	entries := make([]dto.Entry, 0, deletions)
	var wantBytes int64
	for i := 0; i < deletions; i++ {
		size := int64(i + 1)
		wantBytes += size
		entries = append(entries, dto.Entry{Path: fmt.Sprintf("f%03d.txt", i), Size: size})
	}

	t.Run("over limit is estimated", func(t *testing.T) {
		req := testRequest(ModeMirror)
		req.MaxEntries = 10
		res, err := Plan(context.Background(), req, fakeWalk(), &fakeRemote{entries: entries})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if !res.Estimated {
			t.Fatal("Estimated = false, want true")
		}
		if len(res.Warnings) == 0 {
			t.Fatal("expected a truncation warning")
		}
		if len(res.Deletions) != 10 {
			t.Fatalf("len(Deletions) = %d, want 10", len(res.Deletions))
		}
		if res.DeleteCount != deletions || res.DeleteBytes != wantBytes {
			t.Fatalf("counts = %d/%d, want %d/%d",
				res.DeleteCount, res.DeleteBytes, deletions, wantBytes)
		}
	})

	t.Run("under limit keeps 200 display cap", func(t *testing.T) {
		req := testRequest(ModeMirror)
		req.MaxEntries = 1000
		res, err := Plan(context.Background(), req, fakeWalk(), &fakeRemote{entries: entries})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if res.Estimated {
			t.Fatal("Estimated = true, want false")
		}
		if len(res.Deletions) != maxReportedDeletions {
			t.Fatalf("len(Deletions) = %d, want %d", len(res.Deletions), maxReportedDeletions)
		}
		if res.DeleteCount != deletions || res.DeleteBytes != wantBytes {
			t.Fatalf("counts = %d/%d, want %d/%d",
				res.DeleteCount, res.DeleteBytes, deletions, wantBytes)
		}
	})
}

func TestTokenRoundTripTamperAndExpiry(t *testing.T) {
	secret := []byte("round-trip-secret")
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := start

	req := Request{
		LocalRoot:   `D:\photos`,
		RemotePath:  "drive:backup/photos",
		Mode:        ModeMirror,
		TokenSecret: secret,
		TokenTTL:    time.Hour,
		Now:         func() time.Time { return now },
	}

	res, err := Plan(context.Background(), req,
		fakeWalk(LocalFile{Path: "a.jpg", Size: 4}), &fakeRemote{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := VerifyToken(secret, res.Token, req); err != nil {
		t.Fatalf("VerifyToken round trip: %v", err)
	}

	// A single changed hex digit must invalidate the signature.
	bad := []byte(res.Token)
	if bad[len(bad)-1] == '0' {
		bad[len(bad)-1] = '1'
	} else {
		bad[len(bad)-1] = '0'
	}
	if err := VerifyToken(secret, string(bad), req); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("tampered token error = %v, want ErrInvalidToken", err)
	}

	if err := VerifyToken([]byte("other-secret"), res.Token, req); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong secret error = %v, want ErrInvalidToken", err)
	}

	changed := req
	changed.RemotePath = "drive:backup/other"
	if err := VerifyToken(secret, res.Token, changed); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("changed request error = %v, want ErrInvalidToken", err)
	}

	if err := VerifyToken(secret, "not-hex", req); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("malformed token error = %v, want ErrInvalidToken", err)
	}

	now = start.Add(time.Hour) // exactly at expiry
	if err := VerifyToken(secret, res.Token, req); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired token error = %v, want ErrTokenExpired", err)
	}
}

func TestWalkLocalSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("world!"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks are not permitted here: %v", err)
	}
	// A symlinked directory must not be descended into. Some Windows setups
	// allow file symlinks but not directory symlinks, so this part is optional.
	_ = os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "linkdir"))

	req := Request{
		LocalRoot:   root,
		RemotePath:  "drive:backup",
		Mode:        ModeAdditive,
		TokenSecret: []byte("s"),
		TokenTTL:    time.Minute,
	}
	res, err := Plan(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Files != 2 {
		t.Fatalf("Files = %d, want 2", res.Files)
	}
	if res.Bytes != int64(len("hello")+len("world!")) {
		t.Fatalf("Bytes = %d, want %d", res.Bytes, len("hello")+len("world!"))
	}
}

func TestWalkLocalSkipsSystemFiles(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"$RECYCLE.BIN", "System Volume Information", "keep"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"pagefile.sys":                     "p",
		"hiberfil.sys":                     "h",
		"$RECYCLE.BIN/old.bin":             "r",
		"System Volume Information/idx.db": "s",
		"keep/data.txt":                    "keep",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res, err := Plan(context.Background(), Request{
		LocalRoot:   root,
		RemotePath:  "drive:backup",
		Mode:        ModeAdditive,
		TokenSecret: []byte("s"),
	}, nil, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Files != 1 || res.Bytes != int64(len("keep")) {
		t.Fatalf("Files/Bytes = %d/%d, want 1/%d", res.Files, res.Bytes, len("keep"))
	}
}

func TestPlanContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := testRequest(ModeAdditive)
	if _, err := Plan(ctx, req, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Plan error = %v, want context.Canceled", err)
	}
}
