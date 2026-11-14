package engine

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"cloudnest/internal/platform"
	"cloudnest/internal/rc"
)

const (
	maxRestartsPerWindow = 3
	restartWindow        = 10 * time.Minute

	gracefulQuitTimeout = 3 * time.Second
	gracefulStopWait    = 5 * time.Second
	killWait            = 2 * time.Second

	encryptedConfigPrefix = "RCLONE_ENCRYPT_V0:"
	ringLines             = 200
)

// engine is the concrete Engine implementation.
type engine struct {
	opts Options

	// verbose and useProxy shape the rclone process environment. They are
	// guarded by opMu because they are read while the process is launched.
	verbose  bool
	useProxy bool

	// opMu serialises lifecycle operations (Start, Stop, Restart) and process
	// replacement. It is never held while waiting for process output.
	opMu sync.Mutex

	// mu protects the fields below. It is never held across I/O.
	mu              sync.Mutex
	status          Status
	client          *rc.Client
	cmd             *exec.Cmd
	job             *platform.JobObject
	processDone     chan struct{}
	processErr      error
	generation      uint64
	expectedStop    bool
	wantRunning     bool
	superviseCancel context.CancelFunc
	restarts        []time.Time
	binary          string
	version         VersionInfo
	configPath      string

	subs       map[uint64]func(Status)
	nextSub    uint64
	notifyCh   chan struct{}
	notifyOnce sync.Once

	ring *RingBuffer
}

// Status returns the current status snapshot.
func (e *engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// ConfigPath returns the config file used by rclone.
func (e *engine) ConfigPath() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.configPath != "" {
		return e.configPath
	}
	return e.status.ConfigPath
}

// ApplyRuntime updates the process options that are only expressible through
// rclone's environment. The values apply to the next start; a running process
// keeps its old environment until Restart is called.
func (e *engine) ApplyRuntime(verbose, useProxy bool) {
	e.opMu.Lock()
	e.verbose = verbose
	e.useProxy = useProxy
	e.opMu.Unlock()
}

// Client returns the RC client when the engine is ready.
func (e *engine) Client() (*rc.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil || e.status.State != StateReady {
		return nil, ErrNotReady
	}
	return e.client, nil
}

// Subscribe registers a callback. The callback receives the latest status
// from a dedicated notifier goroutine; notifications are coalesced so a slow
// subscriber cannot block the engine. The returned function is idempotent.
func (e *engine) Subscribe(fn func(Status)) (unsubscribe func()) {
	if fn == nil {
		return func() {}
	}
	e.ensureNotifier()

	e.mu.Lock()
	id := e.nextSub
	e.nextSub++
	e.subs[id] = fn
	e.mu.Unlock()

	// Deliver the current snapshot to the new subscriber promptly.
	e.signal()

	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Lock()
			delete(e.subs, id)
			e.mu.Unlock()
		})
	}
}

// Start launches rclone and waits until its RC server is healthy.
func (e *engine) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	state := e.status.State
	e.mu.Unlock()

	switch state {
	case StateReady, StateRestarting:
		// A healthy process exists, or the supervisor owns the lifecycle.
		return nil
	}
	return e.startLocked(ctx)
}

// Stop gracefully shuts down rclone.
func (e *engine) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()

	err := e.stopProcessLocked(ctx)
	version, configPath := e.snapshotStrings()
	e.setStatus(Status{
		State:      StateStopped,
		Version:    version,
		ConfigPath: configPath,
	})
	return err
}

// Restart stops then starts the engine.
func (e *engine) Restart(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()

	e.mu.Lock()
	e.expectedStop = false
	e.mu.Unlock()

	version, configPath := e.snapshotStrings()
	e.setStatus(Status{
		State:      StateRestarting,
		Version:    version,
		ConfigPath: configPath,
	})
	_ = e.stopProcessLocked(ctx)
	return e.startLocked(ctx)
}

// startLocked performs the full startup sequence. opMu must be held.
func (e *engine) startLocked(ctx context.Context) error {
	e.mu.Lock()
	e.restarts = nil
	e.wantRunning = true
	e.expectedStop = false
	e.mu.Unlock()

	e.setStatus(Status{State: StateStarting, ConfigPath: e.ConfigPath()})

	appRoot := e.opts.AppRoot
	if appRoot == "" {
		if root, err := platform.AppRoot(); err == nil {
			appRoot = root
		}
	}

	dataDir := e.opts.DataDir
	if dataDir == "" {
		var err error
		dataDir, err = platform.DataDir()
		if err != nil {
			e.mu.Lock()
			e.wantRunning = false
			e.mu.Unlock()
			return e.failLocked(err)
		}
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		e.mu.Lock()
		e.wantRunning = false
		e.mu.Unlock()
		return e.failLocked(fmt.Errorf("engine: create data directory: %w", err))
	}
	if e.opts.DataDir == "" {
		e.opts.DataDir = dataDir
	}

	configPath := e.opts.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(dataDir, "rclone.conf")
	}
	configPath, err := ensureConfig(dataDir, configPath)
	if err != nil {
		e.mu.Lock()
		e.wantRunning = false
		e.mu.Unlock()
		return e.failLocked(err)
	}

	bin := e.opts.RclonePath
	var info VersionInfo
	if bin != "" {
		info, err = Version(ctx, bin)
	} else {
		bin, info, err = ResolveBinary(ctx, appRoot)
	}
	if err != nil {
		e.mu.Lock()
		e.wantRunning = false
		e.mu.Unlock()
		return e.failLocked(err)
	}

	e.mu.Lock()
	e.binary = bin
	e.version = info
	e.configPath = configPath
	e.opts.ConfigPath = configPath
	e.mu.Unlock()

	e.opts.Logger.Debug("engine: starting rclone",
		"binary", bin,
		"version", info.String(),
		"config", configPath,
	)

	if err := e.launchLocked(ctx, bin, info, configPath); err != nil {
		e.mu.Lock()
		e.wantRunning = false
		e.mu.Unlock()
		return e.failLocked(err)
	}

	superviseCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.superviseCancel = cancel
	e.mu.Unlock()
	go e.supervise(superviseCtx)
	return nil
}

// launchLocked starts one rclone rcd process and waits for its health check.
// opMu must be held. On failure every resource for the spawned process is
// cleaned up and the engine's process fields are cleared.
func (e *engine) launchLocked(ctx context.Context, bin string, info VersionInfo, configPath string) error {
	// A relaunch only happens after the previous process has exited (or was
	// stopped and cleared). Close the dead process's job handle before
	// replacing it so repeated restarts cannot leak handles; closing it also
	// reaps any stragglers through KILL_ON_JOB_CLOSE.
	e.mu.Lock()
	staleJob := e.job
	staleClient := e.client
	e.job = nil
	e.client = nil
	e.mu.Unlock()
	if staleJob != nil {
		_ = staleJob.Close()
	}
	if staleClient != nil {
		staleClient.CloseIdleConnections()
	}

	port, err := FreePort(ctx)
	if err != nil {
		return err
	}
	user, err := randomSecret()
	if err != nil {
		return fmt.Errorf("engine: generate RC user: %w", err)
	}
	pass, err := randomSecret()
	if err != nil {
		return fmt.Errorf("engine: generate RC password: %w", err)
	}

	client := rc.New(fmt.Sprintf("http://127.0.0.1:%d", port), user, pass, e.opts.Logger)

	cmd := exec.Command(bin, "rcd")
	cmd.Env = rcloneEnv(port, user, pass, configPath, e.verbose, e.useProxy)
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	platform.HideWindow(cmd.SysProcAttr)

	logw := newLineWriter(e.emitLine)
	cmd.Stdout = logw
	cmd.Stderr = logw

	job, jobErr := platform.NewJobObject()
	if jobErr != nil {
		e.opts.Logger.Warn("engine: job object unavailable; rclone may outlive the GUI",
			"err", Redact(jobErr.Error()))
		job = nil
	}

	if err := cmd.Start(); err != nil {
		if job != nil {
			_ = job.Close()
		}
		logw.Flush()
		return fmt.Errorf("engine: start rclone: %w", err)
	}
	if job != nil {
		if err := job.Assign(cmd.Process.Pid); err != nil {
			e.opts.Logger.Warn("engine: could not assign rclone to the job object",
				"err", Redact(err.Error()))
			_ = job.Close()
			job = nil
		}
	}

	done := make(chan struct{})
	gen := e.nextGeneration()
	e.mu.Lock()
	e.cmd = cmd
	e.job = job
	e.processDone = done
	e.processErr = nil
	e.client = client
	e.expectedStop = false
	e.binary = bin
	e.version = info
	e.configPath = configPath
	e.mu.Unlock()

	go e.waitProcess(cmd, gen, done, logw)

	if err := WaitReady(ctx, client); err != nil {
		_ = cmd.Process.Kill()
		_ = waitDone(context.Background(), done, killWait)
		if job != nil {
			_ = job.Close()
		}
		client.CloseIdleConnections()
		logw.Flush()

		e.mu.Lock()
		if e.generation == gen {
			e.cmd = nil
			e.processDone = nil
			e.client = nil
			e.job = nil
		}
		e.mu.Unlock()
		return fmt.Errorf("engine: rclone failed health check: %w", err)
	}

	e.setStatus(Status{
		State:      StateReady,
		Version:    info.String(),
		Port:       port,
		ConfigPath: configPath,
	})
	e.opts.Logger.Debug("engine: rclone ready", "port", port, "version", info.String())
	return nil
}

// waitProcess reaps one process, flushes its output and closes done.
func (e *engine) waitProcess(cmd *exec.Cmd, gen uint64, done chan struct{}, logw *lineWriter) {
	err := cmd.Wait()
	logw.Flush()

	e.mu.Lock()
	if e.generation == gen {
		e.processErr = err
	}
	e.mu.Unlock()
	close(done)
}

// stopProcessLocked stops the current process and clears the process fields.
// opMu must be held. The engine state is not changed here.
func (e *engine) stopProcessLocked(ctx context.Context) error {
	e.mu.Lock()
	cancel := e.superviseCancel
	e.superviseCancel = nil
	e.wantRunning = false
	e.expectedStop = true
	cmd := e.cmd
	done := e.processDone
	client := e.client
	job := e.job
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if cmd == nil || cmd.Process == nil {
		e.mu.Lock()
		e.cmd = nil
		e.processDone = nil
		e.client = nil
		e.job = nil
		e.mu.Unlock()
		if job != nil {
			_ = job.Close()
		}
		if client != nil {
			client.CloseIdleConnections()
		}
		return nil
	}

	if client != nil {
		quitCtx, cancelQuit := context.WithTimeout(ctx, gracefulQuitTimeout)
		_ = client.Quit(quitCtx)
		cancelQuit()
	}

	if !waitDone(ctx, done, gracefulStopWait) {
		_ = cmd.Process.Kill()
		_ = waitDone(context.Background(), done, killWait)
	}

	if job != nil {
		_ = job.Close()
	}
	if client != nil {
		client.CloseIdleConnections()
	}

	e.mu.Lock()
	if e.cmd == cmd {
		e.cmd = nil
		e.processDone = nil
		e.client = nil
		e.job = nil
	}
	e.mu.Unlock()
	return nil
}

// supervise restarts rclone after unexpected exits. It stops when the engine
// is intentionally stopped or the restart budget is exhausted.
func (e *engine) supervise(ctx context.Context) {
	for {
		e.mu.Lock()
		done := e.processDone
		running := e.wantRunning
		e.mu.Unlock()
		if !running || done == nil {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-done:
		}

		e.mu.Lock()
		running = e.wantRunning
		waitErr := e.processErr
		e.mu.Unlock()
		if !running {
			return
		}
		if !e.handleUnexpectedExit(ctx, waitErr) {
			return
		}
	}
}

// handleUnexpectedExit records a failure, applies backoff and relaunches.
// It returns false when supervision must stop.
func (e *engine) handleUnexpectedExit(ctx context.Context, cause error) bool {
	e.mu.Lock()
	running := e.wantRunning
	e.mu.Unlock()
	if !running {
		return false
	}

	count, tooMany := e.recordRestart()
	if tooMany {
		e.mu.Lock()
		e.wantRunning = false
		e.mu.Unlock()
		version, configPath := e.snapshotStrings()
		e.setStatus(Status{
			State:      StateFailed,
			Version:    version,
			ConfigPath: configPath,
			Err: redactError(fmt.Errorf("engine: rclone exited repeatedly (%d times within %s): %s",
				count, restartWindow, errorText(cause))),
		})
		e.opts.Logger.Warn("engine: giving up after repeated rclone exits",
			"count", count,
			"err", Redact(errorText(cause)),
		)
		return false
	}

	version, configPath := e.snapshotStrings()
	e.setStatus(Status{
		State:      StateRestarting,
		Version:    version,
		ConfigPath: configPath,
		Err:        redactError(fmt.Errorf("engine: rclone exited: %s", errorText(cause))),
	})

	select {
	case <-ctx.Done():
		return false
	case <-time.After(backoffFor(count)):
	}

	e.opMu.Lock()
	e.mu.Lock()
	running = e.wantRunning
	bin := e.binary
	info := e.version
	cfg := e.configPath
	e.mu.Unlock()
	if !running || bin == "" || cfg == "" {
		e.opMu.Unlock()
		return false
	}
	err := e.launchLocked(ctx, bin, info, cfg)
	e.opMu.Unlock()
	if err != nil {
		e.opts.Logger.Warn("engine: restart failed", "err", Redact(err.Error()))
		// The failed launch did not leave a process to wait for; record the
		// failure and retry within the same bounded budget.
		return e.handleUnexpectedExit(ctx, err)
	}
	return true
}

// recordRestart prunes entries older than restartWindow and appends now. It
// returns the number of restarts in the window and whether the budget has been
// exceeded.
func (e *engine) recordRestart() (int, bool) {
	now := time.Now()
	cutoff := now.Add(-restartWindow)

	e.mu.Lock()
	defer e.mu.Unlock()

	kept := e.restarts[:0]
	for _, t := range e.restarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	e.restarts = append(kept, now)
	return len(e.restarts), len(e.restarts) > maxRestartsPerWindow
}

func backoffFor(restarts int) time.Duration {
	switch {
	case restarts <= 1:
		return time.Second
	case restarts == 2:
		return 2 * time.Second
	default:
		return 4 * time.Second
	}
}

// failLocked records a terminal startup failure. opMu must be held.
func (e *engine) failLocked(err error) error {
	if err == nil {
		return nil
	}
	version, configPath := e.snapshotStrings()
	e.setStatus(Status{
		State:      StateFailed,
		Version:    version,
		ConfigPath: configPath,
		Err:        redactError(err),
	})
	e.opts.Logger.Warn("engine: startup failed", "err", Redact(err.Error()))
	return err
}

func (e *engine) snapshotStrings() (version, configPath string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := e.version
	if !(v.Major == 0 && v.Minor == 0 && v.Patch == 0) {
		version = v.String()
	}
	configPath = e.configPath
	if configPath == "" {
		configPath = e.status.ConfigPath
	}
	return version, configPath
}

func (e *engine) nextGeneration() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.generation++
	return e.generation
}

// emitLine redacts one line of rclone output, appends it to the diagnostic
// ring buffer and forwards it to the configured log writer.
func (e *engine) emitLine(line string) {
	line = Redact(line)
	e.ring.Append(line)
	if e.opts.LogWriter != nil {
		func() {
			defer func() { _ = recover() }()
			e.opts.LogWriter(line)
		}()
	}
}

func (e *engine) setStatus(next Status) {
	e.mu.Lock()
	// A supervisor which was already handling an exit can race with Stop. Do
	// not let its transient/terminal state overwrite the intentional stopped
	// state. Start and Restart clear expectedStop before changing state.
	if (next.State == StateRestarting || next.State == StateFailed) &&
		e.expectedStop && !e.wantRunning {
		e.mu.Unlock()
		return
	}
	if next.State == "" {
		next.State = e.status.State
	}
	if next.Since.IsZero() {
		if next.State == e.status.State && !e.status.Since.IsZero() {
			next.Since = e.status.Since
		} else {
			next.Since = time.Now()
		}
	}
	e.status = next
	e.mu.Unlock()

	e.ensureNotifier()
	e.signal()
}

func (e *engine) ensureNotifier() {
	e.notifyOnce.Do(func() {
		go e.notifyLoop()
	})
}

func (e *engine) signal() {
	select {
	case e.notifyCh <- struct{}{}:
	default:
	}
}

func (e *engine) notifyLoop() {
	for range e.notifyCh {
		e.mu.Lock()
		status := e.status
		subs := make([]func(Status), 0, len(e.subs))
		for _, fn := range e.subs {
			subs = append(subs, fn)
		}
		e.mu.Unlock()

		for _, fn := range subs {
			func() {
				defer func() { _ = recover() }()
				fn(status)
			}()
		}
	}
}

// randomSecret returns 32 random bytes encoded as URL-safe base64. It is used
// for the RC user name and password; the value is never logged.
func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// rcloneEnv builds the rclone process environment, replacing any inherited
// RCLONE_* settings which would conflict with Cloudnest's isolated instance.
//
// Proxy handling: rclone honours HTTP_PROXY/HTTPS_PROXY/NO_PROXY, and inherited
// values would silently win over the Settings toggle, so they are always
// replaced. With useProxy the endpoints come from the Windows Internet
// Settings (or, on other platforms, stay empty and Go falls back to the
// environment); without it NO_PROXY=* sends every request direct.
func rcloneEnv(port int, user, pass, configPath string, verbose, useProxy bool) []string {
	level := "NOTICE"
	if verbose {
		level = "DEBUG"
	}
	overrides := map[string]string{
		"RCLONE_RC_ADDR":    fmt.Sprintf("127.0.0.1:%d", port),
		"RCLONE_RC_USER":    user,
		"RCLONE_RC_PASS":    pass,
		"RCLONE_RC_NO_AUTH": "false",
		"RCLONE_CONFIG":     configPath,
		"RCLONE_LOG_LEVEL":  level,
		"RCLONE_STATS":      "0",

		// Empty values remove the inherited variable (see mergeEnv).
		"HTTP_PROXY":  "",
		"HTTPS_PROXY": "",
		"ALL_PROXY":   "",
		"NO_PROXY":    "",
	}
	if useProxy {
		httpProxy, httpsProxy, noProxy := platform.SystemProxy()
		if httpProxy != "" {
			overrides["HTTP_PROXY"] = httpProxy
		}
		if httpsProxy != "" {
			overrides["HTTPS_PROXY"] = httpsProxy
		}
		if noProxy != "" {
			overrides["NO_PROXY"] = noProxy
		}
	} else {
		overrides["NO_PROXY"] = "*"
	}
	return mergeEnv(os.Environ(), overrides)
}

func mergeEnv(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if _, replaced := overrides[strings.ToUpper(key)]; replaced {
			continue
		}
		out = append(out, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key, value := range overrides {
		if value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}

// ensureConfig makes sure the Cloudnest-owned rclone config exists. When it
// does not, an existing unencrypted %APPDATA%\rclone\rclone.conf is copied
// once. Encrypted configs are never imported.
func ensureConfig(dataDir, configPath string) (string, error) {
	if configPath == "" {
		configPath = filepath.Join(dataDir, "rclone.conf")
	}
	if _, err := os.Stat(configPath); err == nil {
		return configPath, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("engine: inspect rclone config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return "", fmt.Errorf("engine: create config directory: %w", err)
	}

	source := userRcloneConfigPath()
	if source == "" {
		return configPath, nil
	}
	if _, err := os.Stat(source); err != nil {
		// No user config to import; rclone will create the file on demand.
		return configPath, nil
	}
	encrypted, err := configEncrypted(source)
	if err != nil {
		return "", fmt.Errorf("engine: read existing rclone config: %w", err)
	}
	if encrypted {
		return configPath, nil
	}
	if err := copyFile(source, configPath); err != nil {
		return "", fmt.Errorf("engine: copy existing rclone config: %w", err)
	}
	return configPath, nil
}

func userRcloneConfigPath() string {
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "rclone", "rclone.conf")
	}
	if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
		return filepath.Join(cfgDir, "rclone", "rclone.conf")
	}
	return ""
}

func configEncrypted(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, len(encryptedConfigPrefix))
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return strings.HasPrefix(string(buf[:n]), encryptedConfigPrefix), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".rclone.conf-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// redactedError is used for Status.Err so diagnostics never expose OAuth
// URLs or token values.
type redactedError struct{ text string }

func (e redactedError) Error() string { return e.text }

func redactError(err error) error {
	if err == nil {
		return nil
	}
	return redactedError{text: Redact(err.Error())}
}

func errorText(err error) string {
	if err == nil {
		return "process exited"
	}
	return err.Error()
}

func waitDone(ctx context.Context, done <-chan struct{}, timeout time.Duration) bool {
	if done == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}
