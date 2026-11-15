package rc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL, "user", "pass", nil), srv
}

func TestNormalizeBase(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:5572/": "http://127.0.0.1:5572",
		"127.0.0.1:5572":         "http://127.0.0.1:5572",
		" https://host/base/ ":   "https://host/base",
		"":                       "",
	}
	for in, want := range cases {
		if got := normalizeBase(in); got != want {
			t.Errorf("normalizeBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewUsesPerCallTimeout(t *testing.T) {
	client := New("127.0.0.1:1", "", "", nil)
	if client.httpClient.Timeout != 0 {
		t.Fatalf("http.Client.Timeout = %v; it would cap WithTimeout", client.httpClient.Timeout)
	}
}

func TestCallSuccessPostsJSONWithBasicAuth(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/core/version" {
			t.Errorf("path = %s, want /core/version", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "user" || pass != "pass" {
			t.Errorf("BasicAuth = %q/%q/%v", user, pass, ok)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body) != 0 {
			t.Errorf("body = %v, want {}", body)
		}
		io.WriteString(w, `{"version":"v1.70.3","os":"windows"}`)
	})

	var version Version
	if err := client.Call(context.Background(), "core/version", nil, &version); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if version.Version != "v1.70.3" || version.OS != "windows" {
		t.Fatalf("decoded version = %+v", version)
	}
}

func TestCallAuthError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "401 Unauthorized")
	})
	err := client.Call(context.Background(), "core/version", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsAuth(err) {
		t.Fatalf("IsAuth(%v) = false", err)
	}
	var rcErr *Error
	if !errors.As(err, &rcErr) || rcErr.Status != 401 {
		t.Fatalf("error = %#v", err)
	}
}

func TestCallMapsJSONError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"couldn't find method \"config/oauthstatus\"","input":{"name":"x"},"path":"config/oauthstatus","status":404}`)
	})
	err := client.Call(context.Background(), "config/oauthstatus", map[string]any{"name": "x"}, nil)
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false", err)
	}
	var rcErr *Error
	if !errors.As(err, &rcErr) {
		t.Fatalf("not *Error: %T", err)
	}
	if rcErr.Path != "config/oauthstatus" || rcErr.Status != 404 {
		t.Fatalf("error = %+v", rcErr)
	}
	if rcErr.Input["name"] != "x" {
		t.Fatalf("input = %v", rcErr.Input)
	}
	if strings.Contains(rcErr.Error(), `"name"`) {
		t.Fatalf("Error() leaked input: %q", rcErr.Error())
	}
}

func TestCallRetriesIdempotentOnEOF(t *testing.T) {
	var calls int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("ResponseWriter is not a Hijacker")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("Hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		io.WriteString(w, `{"version":"v1.70.3"}`)
	})

	var out Version
	err := client.Call(context.Background(), "core/version", nil, &out, WithRetry(1))
	if err != nil {
		t.Fatalf("Call with retry: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestCallDoesNotRetryNonIdempotent(t *testing.T) {
	var calls int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("ResponseWriter is not a Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		_ = conn.Close()
	})

	err := client.Call(context.Background(), "sync/copy", map[string]any{"srcFs": "a", "dstFs": "b"}, nil, WithRetry(3))
	if err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

func TestCallEmptyResponseWithOutput(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := client.Call(context.Background(), "core/quit", nil, &Version{}); err != nil {
		t.Fatalf("Call: %v", err)
	}
}

func TestListSendsRemoteAndOpt(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if _, ok := body["remote"]; !ok {
			t.Error("request omitted remote")
		}
		if body["remote"] != "" {
			t.Errorf("remote = %v, want empty string", body["remote"])
		}
		if body["fs"] != "drive:" {
			t.Errorf("fs = %v", body["fs"])
		}
		opt, ok := body["opt"].(map[string]any)
		if !ok || opt["showHash"] != true {
			t.Errorf("opt = %v", body["opt"])
		}
		io.WriteString(w, `{"list":[{"Path":"a.txt","Name":"a.txt","Size":6,"ModTime":"2026-11-14T08:09:38+08:00","IsDir":false,"Hashes":{"sha256":"abc"}}]}`)
	})

	entries, err := client.List(context.Background(), "drive:", "", ListOpt{ShowHash: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" || entries[0].Hashes["sha256"] != "abc" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].ModTime.IsZero() {
		t.Fatal("ModTime was not decoded")
	}
}

func TestStatNull(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"item":null}`)
	})
	entry, err := client.Stat(context.Background(), "drive:", "missing", ListOpt{})
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if entry != nil {
		t.Fatalf("entry = %+v, want nil", entry)
	}
}

func TestStartSyncBuildsControlFields(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/copy" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if body["_async"] != true {
			t.Errorf("_async = %v", body["_async"])
		}
		group, _ := body["_group"].(string)
		if group == "" {
			t.Error("_group is empty")
		}
		cfg, ok := body["_config"].(map[string]any)
		if !ok {
			t.Fatalf("_config = %v", body["_config"])
		}
		if cfg["Transfers"] != float64(4) || cfg["Checkers"] != float64(2) {
			t.Errorf("_config = %v", cfg)
		}
		if _, ok := cfg["DryRun"]; !ok {
			t.Errorf("_config missing DryRun: %v", cfg)
		}
		if body["srcFs"] != "src:" || body["dstFs"] != "dst:" {
			t.Errorf("paths = %v / %v", body["srcFs"], body["dstFs"])
		}
		io.WriteString(w, `{"jobid":42}`)
	})

	jobID, err := client.StartSync(context.Background(), "copy", SyncIn{
		SrcFs: "src:", DstFs: "dst:", Group: "g1", Transfers: 4, Checkers: 2,
	})
	if err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if jobID != 42 {
		t.Fatalf("jobID = %d", jobID)
	}
}

func TestStartSyncSendsExcludes(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		filter, ok := body["_filter"].(map[string]any)
		if !ok {
			t.Fatalf("_filter = %v, want map", body["_filter"])
		}
		excludes, ok := filter["ExcludeRule"].([]any)
		if !ok || len(excludes) != 2 || excludes[0] != "*.tmp" || excludes[1] != "node_modules" {
			t.Errorf("ExcludeRule = %#v, want the caller's patterns", filter["ExcludeRule"])
		}
		if filter["IgnoreCase"] != true {
			t.Errorf("IgnoreCase = %#v, want true so matching follows the planner", filter["IgnoreCase"])
		}
		if _, ok := filter["Exclude"]; ok {
			t.Errorf("_filter carries the unrecognised Exclude key: %#v", filter)
		}
		io.WriteString(w, `{"jobid":7}`)
	})

	jobID, err := client.StartSync(context.Background(), "sync", SyncIn{
		SrcFs: "src:", DstFs: "dst:", Exclude: []string{"*.tmp", "node_modules"},
	})
	if err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if jobID != 7 {
		t.Fatalf("jobID = %d", jobID)
	}
}

func TestStartSyncOmitsEmptyFilter(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if value, ok := body["_filter"]; ok {
			t.Errorf("_filter = %v, want it omitted when there are no patterns", value)
		}
		io.WriteString(w, `{"jobid":7}`)
	})

	if _, err := client.StartSync(context.Background(), "copy", SyncIn{SrcFs: "a", DstFs: "b"}); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
}

func TestSyncInDoesNotLeakControlFields(t *testing.T) {
	body, err := json.Marshal(SyncIn{SrcFs: "a", DstFs: "b", Group: "g", Transfers: 8, DryRun: true, Exclude: []string{"*.tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"_async", "_group", "_config", "Group", "Transfers", "Exclude", "*.tmp"} {
		if strings.Contains(string(body), key) {
			t.Errorf("SyncIn JSON contains %q: %s", key, body)
		}
	}
}

func TestSyncMethod(t *testing.T) {
	cases := map[string]string{
		"copy":      "sync/copy",
		"sync":      "sync/sync",
		"sync/copy": "sync/copy",
		"sync/sync": "sync/sync",
	}
	for in, want := range cases {
		got, err := syncMethod(in)
		if err != nil || got != want {
			t.Errorf("syncMethod(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := syncMethod("move"); err == nil {
		t.Error("syncMethod(move) succeeded")
	}
}

func TestIsIdempotent(t *testing.T) {
	idempotent := []string{"core/version", "operations/list", "config/update"}
	notIdempotent := []string{"config/create", "sync/copy", "sync/sync", "job/stop", "job/stopgroup"}
	for _, m := range idempotent {
		if !isIdempotent(m) {
			t.Errorf("isIdempotent(%q) = false", m)
		}
	}
	for _, m := range notIdempotent {
		if isIdempotent(m) {
			t.Errorf("isIdempotent(%q) = true", m)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	if !isRetryable(io.EOF) || !isRetryable(io.ErrUnexpectedEOF) {
		t.Error("EOF should be retryable")
	}
	if isRetryable(context.Canceled) || isRetryable(context.DeadlineExceeded) {
		t.Error("context errors should not be retryable")
	}
	if isRetryable(&Error{Status: 500, Message: "boom"}) {
		t.Error("*Error should not be retryable")
	}
}

func TestEncodeInputNil(t *testing.T) {
	body, err := encodeInput(nil)
	if err != nil || string(body) != "{}" {
		t.Fatalf("encodeInput(nil) = %s, %v", body, err)
	}
}

func TestCallRespectsContext(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Do not wait for the request context here: httptest.Server.Close
		// waits for handlers, so a handler which blocks on the client would
		// deadlock test cleanup.
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, `{}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := client.Call(ctx, "core/version", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestStartCopyFileAsync(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/copyfile" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if body["srcFs"] != "C:/src" || body["srcRemote"] != "a.zip" {
			t.Errorf("source = %v / %v", body["srcFs"], body["srcRemote"])
		}
		if body["dstFs"] != "gdrive:backup" || body["dstRemote"] != "a.zip" {
			t.Errorf("destination = %v / %v", body["dstFs"], body["dstRemote"])
		}
		if body["_async"] != true || body["_group"] != "gui-job-1" {
			t.Errorf("async control = %v / %v", body["_async"], body["_group"])
		}
		io.WriteString(w, `{"jobid":11}`)
	})

	jobID, err := client.StartCopyFile(context.Background(), CopyFileIn{
		SrcFs: "C:/src", SrcRemote: "a.zip",
		DstFs: "gdrive:backup", DstRemote: "a.zip", Group: "gui-job-1",
	})
	if err != nil {
		t.Fatalf("StartCopyFile: %v", err)
	}
	if jobID != 11 {
		t.Fatalf("jobID = %d", jobID)
	}
}

func TestHashSumFileVersionGate(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantOld bool
	}{
		{name: "1.70 is too old", version: `{"version":"v1.70.3","decomposed":[1,70,3]}`, wantOld: true},
		{name: "1.72 is too old", version: `{"version":"v1.72.1","decomposed":[1,72,1]}`, wantOld: true},
		{name: "1.73.0 works", version: `{"version":"v1.73.0","decomposed":[1,73,0]}`},
		{name: "version string fallback", version: `{"version":"v1.73.2"}`},
		{name: "newer works", version: `{"version":"v1.76.0","decomposed":[1,76,0]}`},
		{name: "suffix is ignored", version: `{"version":"v1.73.0-beta.1234"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var versionCalls, hashCalls int32
			client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/core/version":
					atomic.AddInt32(&versionCalls, 1)
					io.WriteString(w, tc.version)
				case "/operations/hashsumfile":
					atomic.AddInt32(&hashCalls, 1)
					io.WriteString(w, `{"hash":"abc123","hashType":"sha256"}`)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})

			hash, err := client.HashSumFile(context.Background(), "gdrive:backup", "a.txt", "sha256")
			if tc.wantOld {
				if !errors.Is(err, ErrHashSumFileUnsupported) {
					t.Fatalf("HashSumFile() error = %v, want ErrHashSumFileUnsupported", err)
				}
				if !strings.Contains(err.Error(), "1.73") {
					t.Fatalf("error = %q, want it to name the required version", err)
				}
				if got := atomic.LoadInt32(&hashCalls); got != 0 {
					t.Fatalf("operations/hashsumfile calls = %d, want 0 on an old rclone", got)
				}
			} else {
				if err != nil {
					t.Fatalf("HashSumFile: %v", err)
				}
				if hash != "abc123" {
					t.Fatalf("hash = %q, want abc123", hash)
				}
				if got := atomic.LoadInt32(&hashCalls); got != 1 {
					t.Fatalf("operations/hashsumfile calls = %d, want 1", got)
				}
			}

			// The version probe is cached: a second call must not ask again.
			_, _ = client.HashSumFile(context.Background(), "gdrive:backup", "a.txt", "sha256")
			if got := atomic.LoadInt32(&versionCalls); got != 1 {
				t.Fatalf("core/version calls = %d, want 1 (cached)", got)
			}
		})
	}
}

func TestHashSumFileVersionProbeFailure(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/operations/hashsumfile" {
			t.Error("hashsumfile was called even though the version probe failed")
		}
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "boom")
	})
	_, err := client.HashSumFile(context.Background(), "gdrive:backup", "a.txt", "sha256")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "rclone version") {
		t.Fatalf("error = %q, want it to mention the version probe", err)
	}
}

func TestHashSumFilePassesCallOptions(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/version":
			io.WriteString(w, `{"version":"v1.76.0","decomposed":[1,76,0]}`)
		case "/operations/hashsumfile":
			// Hold the reply longer than the short option timeout. With the
			// 30s default this call would succeed, so a deadline error proves
			// the option reached Call.
			time.Sleep(250 * time.Millisecond)
			io.WriteString(w, `{"hash":"abc123","hashType":"sha256"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	started := time.Now()
	_, err := client.HashSumFile(context.Background(), "gdrive:backup", "a.txt", "sha256",
		WithTimeout(50*time.Millisecond))
	if err == nil {
		t.Fatal("HashSumFile succeeded; the CallOption did not reach Call")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("call took %v, want the per-call option timeout", elapsed)
	}
}
