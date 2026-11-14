package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloudnest/internal/rc"
)

func TestParseVersionOutput(t *testing.T) {
	cases := []struct {
		name   string
		output string
		major  int
		minor  int
		patch  int
		suffix string
		wantOK bool
	}{
		{
			name:   "release",
			output: "rclone v1.70.3\n- os/version: Microsoft Windows 11 Pro\n- os/arch: amd64\n",
			major:  1, minor: 70, patch: 3,
			wantOK: true,
		},
		{
			name:   "with stderr noise",
			output: "NOTICE: something\nrclone v1.71.0-beta.1234\n",
			major:  1, minor: 71, patch: 0, suffix: "-beta.1234",
			wantOK: true,
		},
		{
			name:   "invalid",
			output: "rclone: not a version\n",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := parseVersionOutput(tc.output)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("parseVersionOutput: %v", err)
				}
				if info.Major != tc.major || info.Minor != tc.minor || info.Patch != tc.patch || info.Suffix != tc.suffix {
					t.Fatalf("info = %+v", info)
				}
			} else if err == nil {
				t.Fatalf("expected error, got %+v", info)
			}
		})
	}
}

func TestVersionInfoAtLeast(t *testing.T) {
	v := VersionInfo{Major: 1, Minor: 70, Patch: 3}
	if !v.AtLeast(1, 70, 0) || !v.AtLeast(1, 70, 3) {
		t.Fatal("1.70.3 should satisfy 1.70.x")
	}
	if v.AtLeast(1, 70, 4) {
		t.Fatal("1.70.3 should not satisfy 1.70.4")
	}
	if v.AtLeast(2, 0, 0) {
		t.Fatal("1.70.3 should not satisfy 2.0.0")
	}
	if (VersionInfo{Major: 1, Minor: 69, Patch: 9}).AtLeast(1, 70, 0) {
		t.Fatal("1.69.9 should be too old")
	}
	if got := (VersionInfo{Major: 1, Minor: 71, Patch: 0, Suffix: "-beta"}).String(); got != "v1.71.0-beta" {
		t.Fatalf("String = %q", got)
	}
}

func TestFindBinaryOrder(t *testing.T) {
	root := t.TempDir()
	pathDir := t.TempDir()
	localRoot := t.TempDir()
	t.Setenv("LOCALAPPDATA", localRoot)
	t.Setenv("PATH", pathDir)

	name := binaryName()
	writeFile(t, filepath.Join(root, "bin", name), "app")
	writeFile(t, filepath.Join(pathDir, name), "path")
	writeFile(t, filepath.Join(localRoot, "Cloudnest", "bin", name), "local")

	got, err := FindBinary(root)
	if err != nil {
		t.Fatalf("FindBinary: %v", err)
	}
	if got != filepath.Join(root, "bin", name) {
		t.Fatalf("got %q, want bundled binary", got)
	}

	if err := os.Remove(filepath.Join(root, "bin", name)); err != nil {
		t.Fatal(err)
	}
	got, err = FindBinary(root)
	if err != nil {
		t.Fatalf("FindBinary PATH: %v", err)
	}
	if got != filepath.Join(pathDir, name) {
		t.Fatalf("got %q, want PATH binary", got)
	}

	if err := os.Remove(filepath.Join(pathDir, name)); err != nil {
		t.Fatal(err)
	}
	got, err = FindBinary(root)
	if err != nil {
		t.Fatalf("FindBinary local: %v", err)
	}
	if got != filepath.Join(localRoot, "Cloudnest", "bin", name) {
		t.Fatalf("got %q, want local fallback", got)
	}

	if err := os.Remove(filepath.Join(localRoot, "Cloudnest", "bin", name)); err != nil {
		t.Fatal(err)
	}
	if _, err := FindBinary(root); !errors.Is(err, ErrNoBinary) {
		t.Fatalf("error = %v, want ErrNoBinary", err)
	}
}

func TestResolveBinaryCanceledContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bin", binaryName()), "stub")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := ResolveBinary(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestFreePort(t *testing.T) {
	port, err := FreePort(context.Background())
	if err != nil {
		t.Fatalf("FreePort: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Fatalf("port = %d", port)
	}
}

func TestFreePortCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FreePort(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestBackoffFor(t *testing.T) {
	cases := map[int]time.Duration{
		1:  time.Second,
		2:  2 * time.Second,
		3:  4 * time.Second,
		10: 4 * time.Second,
	}
	for in, want := range cases {
		if got := backoffFor(in); got != want {
			t.Errorf("backoffFor(%d) = %v, want %v", in, got, want)
		}
	}
}

func TestRcloneEnvOverrides(t *testing.T) {
	t.Setenv("RCLONE_CONFIG", "old")
	t.Setenv("RCLONE_RC_ADDR", "old")
	t.Setenv("KEEP_ME", "yes")

	env := rcloneEnv(12345, "rcuser", "rcpass", "C:/cfg/rclone.conf", false, false)
	values := envMap(env)
	if values["RCLONE_RC_ADDR"] != "127.0.0.1:12345" {
		t.Errorf("RCLONE_RC_ADDR = %q", values["RCLONE_RC_ADDR"])
	}
	if values["RCLONE_CONFIG"] != "C:/cfg/rclone.conf" {
		t.Errorf("RCLONE_CONFIG = %q", values["RCLONE_CONFIG"])
	}
	if values["RCLONE_RC_USER"] != "rcuser" || values["RCLONE_RC_PASS"] != "rcpass" {
		t.Error("RC credentials missing")
	}
	if values["RCLONE_RC_NO_AUTH"] != "false" {
		t.Error("RC authentication could be disabled by the inherited environment")
	}
	if values["RCLONE_LOG_LEVEL"] != "NOTICE" || values["RCLONE_STATS"] != "0" {
		t.Error("log/stat environment wrong")
	}
	if values["KEEP_ME"] != "yes" {
		t.Error("unrelated environment lost")
	}
	if countEnvKey(env, "RCLONE_CONFIG") != 1 {
		t.Errorf("RCLONE_CONFIG duplicated: %v", env)
	}
	// Proxy off: every request must go direct, even when the parent process
	// exported a proxy.
	if values["NO_PROXY"] != "*" {
		t.Errorf("NO_PROXY = %q, want *", values["NO_PROXY"])
	}
	if countEnvKey(env, "HTTP_PROXY") != 0 {
		t.Errorf("inherited HTTP_PROXY survived: %v", env)
	}
}

func TestRcloneEnvVerboseAndProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://inherited:8080")

	verbose := envMap(rcloneEnv(1, "u", "p", "cfg", true, false))
	if verbose["RCLONE_LOG_LEVEL"] != "DEBUG" {
		t.Errorf("RCLONE_LOG_LEVEL = %q, want DEBUG", verbose["RCLONE_LOG_LEVEL"])
	}

	// Proxy on: NO_PROXY no longer forces direct connections and the inherited
	// endpoint is dropped in favour of the system setting.
	proxied := envMap(rcloneEnv(1, "u", "p", "cfg", false, true))
	if proxied["NO_PROXY"] == "*" {
		t.Error("system proxy mode must not force direct connections")
	}
	if got := proxied["HTTP_PROXY"]; got == "http://inherited:8080" {
		t.Errorf("HTTP_PROXY = %q, want the system proxy", got)
	}
}

func TestMergeEnvIsCaseInsensitive(t *testing.T) {
	merged := mergeEnv([]string{"rclone_config=old", "PATH=x"}, map[string]string{"RCLONE_CONFIG": "new"})
	if countEnvKey(merged, "RCLONE_CONFIG") != 1 {
		t.Fatalf("merged = %v", merged)
	}
	if envMap(merged)["PATH"] != "x" {
		t.Fatalf("merged = %v", merged)
	}
}

func TestConfigEncrypted(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.conf")
	enc := filepath.Join(dir, "enc.conf")
	writeFile(t, plain, "[drive]\ntype = drive\n")
	writeFile(t, enc, encryptedConfigPrefix+"abcdef")
	writeFile(t, filepath.Join(dir, "short.conf"), "RCLONE_")

	got, err := configEncrypted(plain)
	if err != nil || got {
		t.Fatalf("plain encrypted = %v, %v", got, err)
	}
	got, err = configEncrypted(enc)
	if err != nil || !got {
		t.Fatalf("encrypted = %v, %v", got, err)
	}
	got, err = configEncrypted(filepath.Join(dir, "short.conf"))
	if err != nil || got {
		t.Fatalf("short = %v, %v", got, err)
	}
}

func TestEnsureConfigCopiesOnce(t *testing.T) {
	dataDir := t.TempDir()
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	source := filepath.Join(appData, "rclone", "rclone.conf")
	writeFile(t, source, "[drive]\ntype = drive\n")

	configPath := filepath.Join(dataDir, "rclone.conf")
	got, err := ensureConfig(dataDir, configPath)
	if err != nil {
		t.Fatalf("ensureConfig: %v", err)
	}
	if got != configPath {
		t.Fatalf("path = %q", got)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "type = drive") {
		t.Fatalf("copied config = %q", data)
	}

	// Changing the user config must not overwrite Cloudnest's copy.
	writeFile(t, source, "[other]\ntype = local\n")
	if _, err := ensureConfig(dataDir, configPath); err != nil {
		t.Fatalf("second ensureConfig: %v", err)
	}
	data, _ = os.ReadFile(configPath)
	if strings.Contains(string(data), "type = local") {
		t.Fatalf("config was overwritten: %q", data)
	}
}

func TestEnsureConfigSkipsEncrypted(t *testing.T) {
	dataDir := t.TempDir()
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	source := filepath.Join(appData, "rclone", "rclone.conf")
	writeFile(t, source, encryptedConfigPrefix+"ciphertext")

	configPath := filepath.Join(dataDir, "rclone.conf")
	if _, err := ensureConfig(dataDir, configPath); err != nil {
		t.Fatalf("ensureConfig: %v", err)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("encrypted config was imported: %v", err)
	}
}

func TestWaitReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/core/version" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"version":"v1.70.3"}`)
	}))
	defer srv.Close()

	client := rc.New(srv.URL, "u", "p", nil)
	if err := WaitReady(context.Background(), client); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReadyRejectsAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "401 Unauthorized")
	}))
	defer srv.Close()

	client := rc.New(srv.URL, "u", "p", nil)
	err := WaitReady(context.Background(), client)
	if err == nil {
		t.Fatal("expected error")
	}
	if !rc.IsAuth(err) {
		t.Fatalf("error = %v, want auth failure", err)
	}
}

func TestEngineNewAndSubscribe(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "rclone.conf")
	eng := New(Options{DataDir: dir, ConfigPath: configPath})
	if got := eng.ConfigPath(); got != configPath {
		t.Fatalf("ConfigPath = %q", got)
	}
	if got := eng.Status().State; got != StateStopped {
		t.Fatalf("State = %q", got)
	}
	if _, err := eng.Client(); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Client error = %v", err)
	}

	impl := eng.(*engine)
	ch := make(chan Status, 4)
	unsub := impl.Subscribe(func(s Status) { ch <- s })
	defer unsub()

	select {
	case <-ch: // initial snapshot
	case <-time.After(time.Second):
		t.Fatal("initial status was not delivered")
	}
	impl.setStatus(Status{State: StateFailed, Err: redactedError{text: "boom"}})
	select {
	case status := <-ch:
		if status.State != StateFailed || status.Err == nil {
			t.Fatalf("status = %+v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("status update was not delivered")
	}

	unsub()
	unsub() // idempotent
}

func TestSetStatusAfterStopIgnoresSupervisor(t *testing.T) {
	eng := New(Options{DataDir: t.TempDir()})
	impl := eng.(*engine)
	impl.mu.Lock()
	impl.expectedStop = true
	impl.wantRunning = false
	impl.mu.Unlock()

	impl.setStatus(Status{State: StateStopped})
	impl.setStatus(Status{State: StateRestarting, Err: redactedError{text: "exit"}})
	if got := impl.Status().State; got != StateStopped {
		t.Fatalf("restarting update after stop changed state to %q", got)
	}
	impl.setStatus(Status{State: StateFailed, Err: redactedError{text: "exit"}})
	if got := impl.Status().State; got != StateStopped {
		t.Fatalf("failed update after stop changed state to %q", got)
	}

	// Once a new Start has cleared expectedStop, failures must be reported.
	impl.mu.Lock()
	impl.expectedStop = false
	impl.wantRunning = true
	impl.mu.Unlock()
	impl.setStatus(Status{State: StateFailed, Err: redactedError{text: "startup"}})
	if got := impl.Status().State; got != StateFailed {
		t.Fatalf("startup failure state = %q, want failed", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		if i := strings.IndexByte(entry, '='); i >= 0 {
			out[strings.ToUpper(entry[:i])] = entry[i+1:]
		}
	}
	return out
}

func countEnvKey(env []string, key string) int {
	count := 0
	for _, entry := range env {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		if strings.EqualFold(name, key) {
			count++
		}
	}
	return count
}
