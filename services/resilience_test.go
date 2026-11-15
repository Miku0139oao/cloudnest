package services

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cloudnest/internal/rc"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestUsageSurvivesRestart pins the cold-start fix: a measured quota is written
// to disk, so the account cards no longer say "unavailable" until Drive answers
// again after a restart.
func TestUsageSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s1 := &Service{usagePath: usagePathFor(dir), usage: make(map[string]usageEntry)}
	s1.storeUsage("gdrive", 294, 1000, 706)

	s2 := &Service{usagePath: usagePathFor(dir), usage: make(map[string]usageEntry)}
	s2.loadUsage()

	used, total, ok := s2.staleUsage("gdrive")
	if !ok || used != 294 || total != 1000 {
		t.Fatalf("staleUsage = %d/%d/%v, want 294/1000/true", used, total, ok)
	}
}

// TestAccountUsageSkipsProviderAfterFailure pins the negative cache: the
// frontend retries quota reads and each cold attempt can take a minute on a
// throttled Drive, so a recent failure returns the last measured value without
// another network round trip.
func TestAccountUsageSkipsProviderAfterFailure(t *testing.T) {
	var abouts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/operations/about" {
			atomic.AddInt32(&abouts, 1)
			rcFail(w, http.StatusInternalServerError, "throttled")
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := &Service{
		eng:       &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)},
		log:       testLogger(),
		usage:     map[string]usageEntry{"gdrive": {used: 5, total: 10, free: 5, at: time.Now().Add(-time.Hour)}},
		usageFail: map[string]usageFailure{},
	}
	s.noteUsageFailure("gdrive", context.DeadlineExceeded)

	u := s.AccountUsage("gdrive")
	if u.Err != nil {
		t.Fatalf("AccountUsage err = %+v, want the stale quota", u.Err)
	}
	if u.Used != 5 || u.Total != 10 {
		t.Fatalf("usage = %d/%d, want 5/10", u.Used, u.Total)
	}
	if got := atomic.LoadInt32(&abouts); got != 0 {
		t.Fatalf("about calls = %d, want 0 after a recent failure", got)
	}
}

// TestListCloudSkipsDaemonAfterTimeout pins the listing circuit breaker: after
// the daemon times out, browsing must not spend another 8s on it.
func TestListCloudSkipsDaemonAfterTimeout(t *testing.T) {
	var lists int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/operations/list" {
			atomic.AddInt32(&lists, 1)
			_, _ = w.Write([]byte(`{"list":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := &Service{
		eng: &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)},
		log: testLogger(),
		// No rclone binary: the CLI fallback fails immediately, which proves
		// the call never reached the daemon.
		rclone:     "",
		listFailAt: map[string]time.Time{},
	}
	s.markListFailure("gdrive")

	if res := s.ListCloud("gdrive", ""); res.Err == nil {
		t.Fatal("ListCloud succeeded without a daemon or CLI")
	}
	if got := atomic.LoadInt32(&lists); got != 0 {
		t.Fatalf("daemon list calls = %d, want 0 while the breaker is open", got)
	}
}
