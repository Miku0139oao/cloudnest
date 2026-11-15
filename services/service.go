// Package services is the only Wails-bound layer. Every exported method is
// callable from the Svelte frontend and returns a DTO with an Err field for
// expected failures.
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"cloudnest/internal/apperr"
	"cloudnest/internal/dto"
	"cloudnest/internal/engine"
	"cloudnest/internal/files"
	"cloudnest/internal/oauth"
	"cloudnest/internal/planner"
	"cloudnest/internal/platform"
	"cloudnest/internal/rc"
	"cloudnest/internal/settings"
)

const Version = "0.1.0"

// restartTimeout bounds the restart itself plus the reconciliation which
// follows it. It is deliberately started *after* the mounts have been detached:
// a slow unmount has its own timeout and must not consume the budget the engine
// process swap needs. It is a variable only so tests can shorten it.
var restartTimeout = 30 * time.Second

type jobState struct {
	ID         string
	Group      string
	JobIDs     []int64
	Kind       string
	Title      string
	StartedAt  time.Time
	TotalBytes int64
	Files      int
	// State is the cached lifecycle state: running | paused | done | failed |
	// cancelled | interrupted. "interrupted" means the run is over (or its
	// rclone ids are gone) but its outcome was never recorded: rclone purges
	// successful and failed jobs alike, so it must never be treated as a plain
	// success ("done") nor as a proven failure ("failed").
	State   string
	Phase   string
	Bytes   int64
	Speed   float64
	ETA     *int64
	Current string
	Failed  int
	// Err holds rclone's message for a failed job, or the explanation for an
	// interrupted one, so the UI can say more than a bare status.
	Err       string
	Sources   []string
	DestRoot  string
	AccountID string

	// Split intent. A backup which started the split pipeline cannot be
	// resumed as a whole-file sync without duplicating storage and leaving the
	// .cloudbox-part set incomplete, so ResumeJob rebuilds the plan from these
	// values. PartSizeMB is the part size the job actually started with, not
	// the current setting: a later Settings change must not produce a part
	// layout which differs from the parts already uploaded.
	FastFiles  bool
	Mode       string
	Excludes   []string
	PartSizeMB int
	// SplitActive reports whether the split pipeline was actually in flight
	// (a FastFiles backup may legitimately have no file large enough to split).
	SplitActive bool
	// SplitRunning reports whether runSplitBackup is still executing for this
	// job. It is set when the goroutine is launched and cleared under s.mu when
	// that goroutine exits, so callers can tell a stale cached State from a
	// transfer which really is still in flight.
	SplitRunning bool
	// Resuming is set by ResumeJob for the whole reservation window: from the
	// state flip to "running" (while JobIDs still hold the paused run's ids)
	// until the newly started rclone ids are installed (or the resume fails).
	// No liveness probe during that window says anything about the new run, so
	// every decision must treat the job as in flight: never apply a terminal
	// verdict to it and never start a duplicate scheduled backup.
	Resuming bool
	// StopSeq counts user stop requests (PauseJob/CancelJob), incremented under
	// s.mu before job/stopgroup runs. ResumeJob snapshots it with the
	// reservation and only commits while it is unchanged: a stop which began
	// while the resume was starting new jobs may have reached rclone before
	// those jobs existed, so its stopgroup cannot be relied on to stop them.
	StopSeq int
}

type usageEntry struct {
	used  int64
	total int64
	free  int64
	at    time.Time
}

// usageFailure is a quota attempt which failed, kept for a short while so a
// provider that is throttling is not asked again immediately.
type usageFailure struct {
	at  time.Time
	err error
}

// usageRetryAfter is how long a failed quota read is remembered. The frontend
// retries on its own, and each cold rclone attempt can take tens of seconds.
const usageRetryAfter = 90 * time.Second

// listBreakerWindow is how long listings skip the daemon after it timed out.
// A fresh rclone process answers in about a second where the daemon's wedged
// Drive pacer would take longer than the call timeout.
const listBreakerWindow = 90 * time.Second

type Service struct {
	app      *application.App
	eng      engine.Engine
	store    *settings.Store
	oauth    *oauth.Manager
	rclone   string
	log      *slog.Logger
	dataDir  string
	portable bool

	mu      sync.Mutex
	jobs    map[string]*jobState
	order   []string
	records []dto.Record
	usage   map[string]usageEntry
	// usageFail remembers a recent quota failure per account so a slow provider
	// is not asked again on every card render (see AccountUsage).
	usageFail map[string]usageFailure
	// usagePath is where the last measured quotas are persisted. A cold start
	// shows those numbers instead of "unavailable" until a fresh read lands.
	usagePath string
	usageMu   sync.Mutex
	// listFailAt remembers when the rclone daemon last timed out listing an
	// account. Its Drive pacer can wedge while a fresh process still answers,
	// so listings go straight to the CLI fallback for a while.
	listFailAt map[string]time.Time

	// journalEntries keeps the persisted history with enough detail to resume
	// an interrupted backup after a restart.
	journalEntries []dto.JournalEntry
	journalPath    string
	// journalMu serialises the whole snapshot -> marshal -> write -> rename
	// sequence. The s.mu snapshot alone is not enough: two writers could
	// snapshot in order and then land out of order (or share the .tmp file),
	// leaving an older journal which re-offers a dismissed or finished run.
	journalMu sync.Mutex

	// closing is set once the user confirms stopping an in-flight transfer, so
	// the window close guard lets the second request through.
	closing atomic.Bool

	// restartMu serialises the whole detach -> restart -> reattach sequence.
	// The engine serialises the process swap itself, but two overlapping
	// restarts would otherwise interleave their mount handling and lose a
	// drive letter.
	restartMu sync.Mutex

	// enginePort is the RC port of the rclone process the Service last saw
	// ready. Every launch picks a new port, so a change means the engine was
	// relaunched (including by its own supervisor) and the old job ids are
	// gone. Guarded by mu.
	enginePort int
}

func New(app *application.App, eng engine.Engine, store *settings.Store, rclonePath string, logger *slog.Logger) *Service {
	dataDir, _ := platform.DataDir()
	s := &Service{
		app:        app,
		eng:        eng,
		store:      store,
		rclone:     rclonePath,
		log:        logger,
		dataDir:    dataDir,
		portable:   dataDir != "",
		jobs:       make(map[string]*jobState),
		usage:      make(map[string]usageEntry),
		usageFail:  make(map[string]usageFailure),
		listFailAt: make(map[string]time.Time),
	}
	s.oauth = oauth.New(rclonePath, s.persistToken)
	if logger != nil {
		s.oauth.SetLogf(func(format string, args ...any) { logger.Info(fmt.Sprintf(format, args...)) })
	}
	s.journalPath = journalPathFor(dataDir)
	s.usagePath = usagePathFor(dataDir)
	s.loadUsage()
	s.loadJournal()
	// The engine's supervisor relaunches rclone after an unexpected exit
	// without going through RestartEngine. Reconcile there too, otherwise an
	// upload/download/copy which was in flight stays "running" forever.
	s.watchEngine()
	return s
}

// AttachApp provides the Wails app after application.New has bound the service.
// It is a package function (not a method) so it is not exposed to the frontend.
func AttachApp(s *Service, app *application.App) { s.app = app }

// ---------------------------------------------------------------- bootstrap

func (s *Service) Bootstrap() dto.Bootstrap {
	st := s.eng.Status()
	out := dto.Bootstrap{
		Version:    Version,
		Language:   s.store.Get().Language,
		Theme:      s.store.Get().Theme,
		Onboarded:  s.store.Get().Onboarded,
		Engine:     mapStatus(st),
		DataDir:    s.dataDir,
		PortableOK: s.portable,
	}
	if st.State == engine.StateReady {
		// Accounts are fetched by the frontend once it is ready; keeping
		// Bootstrap fast avoids blocking the first paint on slow providers.
		if limit := s.store.Get().Backup.BwLimit; limit != "" && limit != "off" {
			go s.ApplyBwLimit(limit)
		}
	}
	s.mu.Lock()
	out.Recent = append([]dto.Record(nil), s.records...)
	s.mu.Unlock()
	if len(out.Recent) > 5 {
		out.Recent = out.Recent[:5]
	}
	out.Pending = s.PendingResume()
	return out
}

func (s *Service) EngineStatus() dto.EngineStatus { return mapStatus(s.eng.Status()) }

func (s *Service) RestartEngine() dto.Empty {
	if err := s.restartEngineWithMounts(context.Background()); err != nil {
		if s.log != nil {
			s.log.Warn("engine restart", "err", err)
		}
		// The frontend has no dedicated restart control, but a failure still
		// must not be silent: show it in History and return it to any caller.
		s.recordRestartFailure(err)
		return dto.Empty{Err: failFrom(err)}
	}
	return dto.Empty{}
}

// watchEngine subscribes to the engine's status and reconciles once a new
// rclone process is ready. The supervisor relaunches rclone after an unexpected
// exit, so without this nothing would terminalise the jobs owned by the old
// process. The initial Ready is ignored (there is nothing to reconcile yet).
func (s *Service) watchEngine() {
	if s == nil || s.eng == nil {
		return
	}
	s.eng.Subscribe(func(st engine.Status) {
		if st.State != engine.StateReady || st.Port == 0 {
			return
		}
		s.mu.Lock()
		changed := s.enginePort != 0 && s.enginePort != st.Port
		s.enginePort = st.Port
		s.mu.Unlock()
		if !changed {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), restartTimeout)
			defer cancel()
			s.reconcileAfterRestart(ctx)
		}()
	})
}

// mountSnapshot holds what is needed to mount an active mount again after the
// engine process has been restarted.
type mountSnapshot struct {
	accountID  string
	remotePath string
	mountPoint string
}

// mountSnapshotFrom splits rclone's "remote:path" mount source into the
// account and remote path MountCloud expects. ok is false for a source which
// does not name a remote.
func mountSnapshotFrom(m dto.MountInfo) (mountSnapshot, bool) {
	fs := strings.TrimSpace(m.Fs)
	i := strings.Index(fs, ":")
	if i <= 0 || strings.TrimSpace(m.MountPoint) == "" {
		return mountSnapshot{}, false
	}
	return mountSnapshot{
		accountID:  fs[:i],
		remotePath: strings.Trim(strings.TrimSpace(fs[i+1:]), "/"),
		mountPoint: m.MountPoint,
	}, true
}

// detachMounts unmounts every mount hosted by the engine and returns the
// mounts which are now detached. rclone hosts the mount inside its own process,
// so stopping rclone without this leaves a drive letter pointing at nothing.
// An error means at least one mount is still attached and the engine must not
// be restarted, otherwise that drive would be stranded.
func (s *Service) detachMounts() ([]mountSnapshot, error) {
	list := s.ListMounts()
	if list.Err != nil {
		// A listing failure only means "nothing to detach" when the engine is
		// not running. While it is ready it may still be hosting a mount, so a
		// restart now could strand that drive letter: abort instead.
		if s.engineRunning() {
			detail := list.Err.Detail
			if detail == "" {
				detail = list.Err.Message
			}
			return nil, fmt.Errorf("could not list mounts before restart: %s", detail)
		}
		// If the daemon is not running it cannot be hosting a mount which this
		// restart could strand.
		s.mountWarn("restart: mounts not listed before restart", "err", list.Err.Detail)
		return nil, nil
	}
	out := make([]mountSnapshot, 0, len(list.Items))
	var firstErr error
	for _, m := range list.Items {
		snap, snapOK := mountSnapshotFrom(m)
		if res := s.UnmountCloud(m.MountPoint); res.Err != nil {
			s.mountWarn("restart: unmount before restart failed",
				"mountPoint", m.MountPoint, "err", res.Err.Detail)
			if firstErr == nil {
				firstErr = fmt.Errorf("could not unmount %s: %s", m.MountPoint, res.Err.Message)
			}
			continue
		}
		if snapOK {
			out = append(out, snap)
		}
	}
	return out, firstErr
}

// reattachMounts restores the mounts detached for an engine restart. Failures
// are logged; the user can mount them again from the Clouds page.
func (s *Service) reattachMounts(mounts []mountSnapshot) {
	for _, m := range mounts {
		if res := s.MountCloud(m.accountID, m.remotePath, m.mountPoint); res.Err != nil {
			s.mountWarn("restart: remount failed",
				"mountPoint", m.mountPoint, "account", m.accountID, "path", m.remotePath,
				"err", res.Err.Detail)
		}
	}
}

// restartEngineWithMounts detaches active mounts first and restores them once
// rclone is back. Restarting with a mount still attached would stop the daemon
// hosting it and leave the drive letter dangling.
//
// The whole sequence is serialised: RestartEngine and the SaveSettings
// goroutine can both request a restart, and two interleaved sequences would
// detach/restart/reattach on top of each other and lose a mount.
func (s *Service) restartEngineWithMounts(ctx context.Context) error {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()

	// A caller which queued behind a slow restart must not hand the sequence an
	// already-expired context.
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}

	// Detach the mounts before starting the restart clock. A slow listing or
	// unmount has its own timeouts (up to 10 s + 30 s per mount) and must not
	// consume the budget the engine process swap needs: running eng.Restart with
	// an expired context kills rclone and then fails to start a replacement.
	mounts, unmountErr := s.detachMounts()
	if unmountErr != nil {
		// Keeping the old process is safer than stranding the mount which is
		// still attached; put back the mounts which were already detached.
		s.reattachMounts(mounts)
		return unmountErr
	}

	// The restart gets a fresh budget now that the slow detach is over.
	runCtx, cancel := context.WithTimeout(ctx, restartTimeout)
	defer cancel()
	if err := s.eng.Restart(runCtx); err != nil {
		if len(mounts) > 0 {
			s.mountWarn("restart: engine did not come back; mounts are detached",
				"count", len(mounts))
		}
		return err
	}
	s.reattachMounts(mounts)
	// Jobs which were running in the old rclone process can never finish now:
	// the new process does not know their ids. Reconcile them here so the
	// scheduler's overlap guard is not wedged forever by a stale "running".
	s.reconcileAfterRestart(runCtx)
	return nil
}

// engineRunning reports whether the engine currently has a process which could
// be hosting a mount.
func (s *Service) engineRunning() bool {
	if s == nil || s.eng == nil {
		return false
	}
	return s.eng.Status().State == engine.StateReady
}

func (s *Service) GetSettings() dto.Settings { return s.store.Get() }

func (s *Service) SaveSettings(in dto.Settings) dto.Empty {
	before := s.store.Get().Advanced

	// Keep the scheduler's "already ran" marker unless the schedule itself
	// changed; otherwise every settings save would trigger an extra backup.
	// Enabled and the execution options count as part of the schedule: turning
	// a schedule back on must not fire a catch-up run for a point that passed
	// while it was off, and changing how it runs should likewise take effect
	// from the next point rather than immediately.
	prevSched := s.store.Get().Schedule
	in.Schedule.LastRun = prevSched.LastRun
	if in.Schedule.Enabled != prevSched.Enabled ||
		in.Schedule.Time != prevSched.Time ||
		in.Schedule.AccountID != prevSched.AccountID ||
		in.Schedule.DestRoot != prevSched.DestRoot ||
		in.Schedule.Mode != prevSched.Mode ||
		in.Schedule.FastFiles != prevSched.FastFiles ||
		!sameStrings(in.Schedule.SourcePaths, prevSched.SourcePaths) {
		// A schedule that was just configured must not fire immediately for a
		// time that already passed today; the first run is the next point.
		in.Schedule.LastRun = time.Now().Unix()
	}

	_ = s.store.Update(func(cur *dto.Settings) {
		if in.Language != "" {
			cur.Language = in.Language
		}
		if in.Theme != "" {
			cur.Theme = in.Theme
		}
		cur.Backup = in.Backup
		cur.Schedule = in.Schedule
		cur.Advanced = in.Advanced
	})
	after := s.store.Get().Advanced
	// Proxy and verbose logging live in rclone's environment, so a change can
	// only take effect by relaunching it. Detach active mounts first, then
	// restore them after the new daemon is ready. Restart in the background so
	// the settings dialog stays responsive.
	if after.Proxy != before.Proxy || after.Verbose != before.Verbose {
		s.eng.ApplyRuntime(after.Verbose, after.Proxy)
		go func() {
			// The deadline is created inside restartEngineWithMounts, after it
			// has the serialisation lock, so a queued restart is not born
			// expired.
			if err := s.restartEngineWithMounts(context.Background()); err != nil {
				if s.log != nil {
					s.log.Warn("restart after settings change", "err", err)
				}
				// SaveSettings already returned "saved"; make the failure to
				// apply the new proxy/verbose value visible.
				s.recordRestartFailure(err)
			}
		}()
	}
	return dto.Empty{}
}

func (s *Service) SetLanguage(lang string) dto.Empty {
	_ = s.store.Update(func(cur *dto.Settings) { cur.Language = lang })
	return dto.Empty{}
}

func (s *Service) SetTheme(theme string) dto.Empty {
	_ = s.store.Update(func(cur *dto.Settings) { cur.Theme = theme })
	return dto.Empty{}
}

func (s *Service) MarkOnboarded() dto.Empty {
	_ = s.store.Update(func(cur *dto.Settings) { cur.Onboarded = true })
	return dto.Empty{}
}

// ---------------------------------------------------------------- dialogs

func (s *Service) ChooseFolder(title string) dto.PathPick {
	if s.app == nil {
		return dto.PathPick{Cancelled: true}
	}
	p, err := s.app.Dialog.OpenFile().
		SetTitle(title).
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()
	if err != nil {
		return dto.PathPick{Cancelled: true}
	}
	return dto.PathPick{Path: p}
}

func (s *Service) ChooseFiles(title string) dto.PathPick {
	if s.app == nil {
		return dto.PathPick{Cancelled: true}
	}
	ps, err := s.app.Dialog.OpenFile().
		SetTitle(title).
		CanChooseDirectories(false).
		CanChooseFiles(true).
		AllowsOtherFileTypes(true).
		PromptForMultipleSelection()
	if err != nil {
		return dto.PathPick{Cancelled: true}
	}
	return dto.PathPick{Paths: ps}
}

func (s *Service) OpenLogFolder() dto.Empty {
	if dir, err := platform.LogDir(); err == nil {
		_ = exec.Command("explorer.exe", dir).Start()
	}
	return dto.Empty{}
}

func (s *Service) OpenConfigFolder() dto.Empty {
	if dir, err := platform.DataDir(); err == nil {
		_ = exec.Command("explorer.exe", dir).Start()
	}
	return dto.Empty{}
}

func (s *Service) OpenExternal(url string) dto.Empty {
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		_ = platform.OpenURL(url)
	}
	return dto.Empty{}
}

func (s *Service) WindowMinimise() dto.Empty {
	if w, ok := s.window(); ok {
		w.Minimise()
	}
	return dto.Empty{}
}

func (s *Service) WindowToggleMaximise() dto.Empty {
	if w, ok := s.window(); ok {
		w.ToggleMaximise()
	}
	return dto.Empty{}
}

func (s *Service) WindowClose() dto.Empty {
	if w, ok := s.window(); ok {
		w.Close()
	}
	return dto.Empty{}
}

func (s *Service) Quit() dto.Empty {
	if s.app != nil {
		s.app.Quit()
	}
	return dto.Empty{}
}

// ConfirmClose is the "stop and quit" answer to the close warning: it marks
// the close as intentional and quits, so the window hook steps aside.
func (s *Service) ConfirmClose() dto.Empty {
	AllowClose(s)
	if s.app != nil {
		s.app.Quit()
	}
	return dto.Empty{}
}

// DebugLog lets the frontend write a line to the application log.
func (s *Service) DebugLog(msg string) dto.Empty {
	s.log.Info("frontend: " + msg)
	return dto.Empty{}
}

// ApplyBwLimit sets the rclone-wide bandwidth limit (e.g. "off", "1M").
func (s *Service) ApplyBwLimit(rate string) dto.Empty {
	c, fail := s.client()
	if fail != nil {
		return dto.Empty{}
	}
	if strings.TrimSpace(rate) == "" {
		rate = "off"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Call(ctx, "core/bwlimit", map[string]any{"rate": rate}, nil)
	return dto.Empty{}
}

func (s *Service) window() (application.Window, bool) {
	if s.app == nil {
		return nil, false
	}
	w, ok := s.app.Window.GetByName("main")
	if !ok {
		return nil, false
	}
	return w, true
}

// ---------------------------------------------------------------- accounts

func (s *Service) ListAccounts() dto.AccountList {
	c, fail := s.client()
	if fail != nil {
		return dto.AccountList{Err: fail}
	}
	started := time.Now()
	listCtx, listCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer listCancel()
	remotes, err := c.ListRemotes(listCtx, false)
	if err != nil {
		s.log.Warn("service: list remotes failed", "err", err)
		return dto.AccountList{Err: failFrom(err)}
	}
	out := make([]dto.Account, len(remotes))
	for i, r := range remotes {
		typeCtx, typeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		provider, sharedClientID := s.remoteConfig(typeCtx, c, r.Name)
		typeCancel()
		out[i] = dto.Account{
			ID: r.Name, Provider: provider, Name: r.Name, Status: "connected",
			SharedClientID: sharedClientID,
		}
		// Show the last measured quota immediately: the fresh read is done per
		// card by AccountUsage and can take tens of seconds on a throttled
		// Drive. Without a persisted value the cards said "unavailable" on
		// every cold start even though a quota had been measured before.
		if used, total, ok := s.staleUsage(r.Name); ok {
			out[i].Used, out[i].Total = used, total
		}
		s.mu.Lock()
		for _, rec := range s.records {
			if rec.State == "done" && rec.FinishedAt > out[i].LastBackup {
				out[i].LastBackup = rec.FinishedAt
			}
		}
		s.mu.Unlock()
	}

	// Usage is intentionally NOT queried here: operations/about can be slow on
	// a cold backend and must not block the account list. The frontend calls
	// AccountUsage per card instead, and the result is cached for ten minutes.
	if !s.store.Get().Onboarded && len(out) > 0 {
		_ = s.store.Update(func(cur *dto.Settings) { cur.Onboarded = true })
	}
	s.log.Info("service: accounts loaded", "count", len(out), "took", time.Since(started).String())
	return dto.AccountList{Items: out}
}

func (s *Service) AccountUsage(id string) dto.Usage {
	if used, total, ok := s.cachedUsage(id); ok {
		return dto.Usage{Used: used, Total: total, Free: total - used}
	}

	// A provider that just failed must not be asked again straight away:
	// operations/about on a throttled Drive takes tens of seconds, and the
	// frontend (plus its own retry) would otherwise queue several of them.
	if fail, ok := s.recentUsageFailure(id); ok {
		if used, total, ok := s.staleUsage(id); ok {
			return dto.Usage{Used: used, Total: total, Free: total - used}
		}
		return dto.Usage{Err: failFrom(fail.err)}
	}

	// Quota is decorative: a slow or throttled provider must not turn into a
	// permanent "unavailable" label, so try the daemon once, then a fresh
	// rclone process, and finally fall back to whatever was measured before.
	var lastErr error
	if c, fail := s.client(); fail == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		u, err := c.About(ctx, id+":")
		cancel()
		if err == nil && u.Total > 0 {
			s.storeUsage(id, u.Used, u.Total, u.Free)
			return dto.Usage{Used: u.Used, Total: u.Total, Free: u.Free}
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New("cloud did not report a quota")
		}
	} else {
		lastErr = errors.New(fail.Message)
	}

	if used, total, free, err := s.aboutViaCLI(id); err == nil {
		s.storeUsage(id, used, total, free)
		return dto.Usage{Used: used, Total: total, Free: free}
	} else if lastErr == nil {
		lastErr = err
	}

	if lastErr != nil {
		s.noteUsageFailure(id, lastErr)
	}
	if used, total, ok := s.staleUsage(id); ok {
		return dto.Usage{Used: used, Total: total, Free: total - used}
	}
	return dto.Usage{Err: failFrom(lastErr)}
}

func (s *Service) recentUsageFailure(id string) (usageFailure, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.usageFail[id]
	if !ok || time.Since(f.at) > usageRetryAfter {
		return usageFailure{}, false
	}
	return f, true
}

func (s *Service) noteUsageFailure(id string, err error) {
	s.mu.Lock()
	if s.usageFail == nil {
		s.usageFail = make(map[string]usageFailure)
	}
	s.usageFail[id] = usageFailure{at: time.Now(), err: err}
	s.mu.Unlock()
}

// listBreakerOpen reports whether the daemon timed out listing this account
// recently enough that listings should go straight to the CLI fallback.
func (s *Service) listBreakerOpen(accountID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.listFailAt[accountID]
	if !ok {
		return false
	}
	if time.Since(at) > listBreakerWindow {
		delete(s.listFailAt, accountID)
		return false
	}
	return true
}

func (s *Service) markListFailure(accountID string) {
	s.mu.Lock()
	if s.listFailAt == nil {
		s.listFailAt = make(map[string]time.Time)
	}
	s.listFailAt[accountID] = time.Now()
	s.mu.Unlock()
}

func (s *Service) clearListFailure(accountID string) {
	s.mu.Lock()
	delete(s.listFailAt, accountID)
	s.mu.Unlock()
}

func (s *Service) cachedUsage(id string) (int64, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.usage[id]
	if !ok || time.Since(u.at) > 10*time.Minute {
		return 0, 0, false
	}
	return u.used, u.total, true
}

// staleUsage returns the last successfully measured quota regardless of age.
func (s *Service) staleUsage(id string) (int64, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.usage[id]
	if !ok || u.total <= 0 {
		return 0, 0, false
	}
	return u.used, u.total, true
}

func (s *Service) storeUsage(id string, used, total, free int64) {
	s.mu.Lock()
	if s.usage == nil {
		s.usage = make(map[string]usageEntry)
	}
	s.usage[id] = usageEntry{used: used, total: total, free: free, at: time.Now()}
	delete(s.usageFail, id)
	s.mu.Unlock()
	s.saveUsage()
}

// usagePathFor mirrors journalPathFor: the file lives beside history.json, and
// an ephemeral data dir simply means quotas are not persisted.
func usagePathFor(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "usage.json")
}

// loadUsage restores the last measured quotas so the account cards show real
// numbers on a cold start, when Drive routinely takes longer than the call
// timeout to report them.
func (s *Service) loadUsage() {
	if s.usagePath == "" {
		return
	}
	b, err := os.ReadFile(s.usagePath)
	if err != nil {
		return
	}
	var raw map[string]struct {
		Used  int64 `json:"used"`
		Total int64 `json:"total"`
		Free  int64 `json:"free"`
		At    int64 `json:"at"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, u := range raw {
		if u.Total <= 0 {
			continue
		}
		s.usage[id] = usageEntry{used: u.Used, total: u.Total, free: u.Free, at: time.Unix(u.At, 0)}
	}
}

// saveUsage writes every measured quota atomically. The write is serialised so
// two accounts finishing at once cannot interleave into a corrupt file.
func (s *Service) saveUsage() {
	if s.usagePath == "" {
		return
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()

	s.mu.Lock()
	raw := make(map[string]struct {
		Used  int64 `json:"used"`
		Total int64 `json:"total"`
		Free  int64 `json:"free"`
		At    int64 `json:"at"`
	}, len(s.usage))
	for id, u := range s.usage {
		raw[id] = struct {
			Used  int64 `json:"used"`
			Total int64 `json:"total"`
			Free  int64 `json:"free"`
			At    int64 `json:"at"`
		}{Used: u.used, Total: u.total, Free: u.free, At: u.at.Unix()}
	}
	s.mu.Unlock()

	b, err := json.Marshal(raw)
	if err != nil {
		return
	}
	tmp := s.usagePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, s.usagePath); err != nil && s.log != nil {
		s.log.Warn("service: persist usage failed", "err", err)
	}
}

func (s *Service) ListProviders() dto.ProviderCatalog {
	c, fail := s.client()
	if fail != nil {
		return dto.ProviderCatalog{Err: fail}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ps, err := c.Providers(ctx)
	if err != nil {
		return dto.ProviderCatalog{Err: failFrom(err)}
	}
	out := make([]dto.Provider, 0, len(ps))
	for _, p := range ps {
		item := dto.Provider{Name: p.Name, Description: p.Description, Prefix: p.Prefix}
		for _, o := range p.Options {
			item.Options = append(item.Options, dto.ProviderOption{
				Name:      o.Name,
				Type:      o.Type,
				Default:   fmt.Sprint(o.Default),
				Help:      o.Help,
				Required:  o.Required,
				Password:  o.IsPassword,
				Advanced:  o.Advanced,
				Sensitive: o.Sensitive,
				Provider:  o.Provider,
			})
			for _, ex := range o.Examples {
				item.Options[len(item.Options)-1].Examples = append(
					item.Options[len(item.Options)-1].Examples,
					dto.ProviderExample{Value: ex.Value, Help: ex.Help},
				)
			}
		}
		out = append(out, item)
	}
	return dto.ProviderCatalog{Items: out}
}

func (s *Service) BeginConnect(provider, displayName string) dto.ConnectSession {
	if _, fail := s.client(); fail != nil {
		return dto.ConnectSession{Err: fail}
	}
	typ := rcloneType(provider)
	displayName = strings.TrimSpace(displayName)
	name := displayName
	if name == "" {
		name = s.uniqueRemoteName(defaultRemoteName(provider))
	}
	// A non-empty displayName comes from "sign in again" and must keep its
	// exact name; uniquifying it would add a second remote instead of
	// refreshing the existing one.
	id, err := s.oauth.Begin(typ, name)
	if err != nil {
		switch {
		case errors.Is(err, oauth.ErrBusy):
			return dto.ConnectSession{Err: &dto.Fail{Code: string(apperr.OAuthBusy), Message: "another sign-in is running"}}
		case errors.Is(err, oauth.ErrPortBusy):
			return dto.ConnectSession{Err: &dto.Fail{
				Code:    string(apperr.OAuthPortBusy),
				Message: "the sign-in port is busy",
				Detail:  "Port 53682 is already in use, usually by an earlier sign-in that did not finish. Close any rclone windows and try again.",
			}}
		}
		return dto.ConnectSession{Err: failFrom(err)}
	}
	return dto.ConnectSession{ID: id, Provider: provider, DisplayName: name}
}

func (s *Service) ConnectState(id string) dto.ConnectProgress {
	p := s.oauth.Poll(id)
	out := dto.ConnectProgress{State: string(p.State), AuthURL: p.URL, Message: p.Message}
	if p.Error != "" {
		out.Err = &dto.Fail{Code: string(apperr.Unknown), Message: "sign-in failed", Detail: p.Error, Retry: true}
	}
	return out
}

func (s *Service) CancelConnect(id string) dto.Empty {
	s.oauth.Cancel(id)
	return dto.Empty{}
}

func (s *Service) RemoveAccount(id string) dto.Empty {
	c, fail := s.client()
	if fail != nil {
		return dto.Empty{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.DeleteRemote(ctx, id)
	return dto.Empty{}
}

func (s *Service) persistToken(ctx context.Context, provider, name, token string) error {
	c, err := s.eng.Client()
	if err != nil {
		return err
	}

	params := map[string]any{"token": token}
	if provider == "onedrive" {
		extra, driveErr := onedriveDriveParams(ctx, token)
		if driveErr != nil {
			if s.log != nil {
				s.log.Warn("onedrive: drive lookup failed", "err", driveErr)
			}
		} else {
			for k, v := range extra {
				params[k] = v
			}
		}
	}

	opt := &rc.ConfigOpt{NonInteractive: true}
	if _, err := c.CreateRemote(ctx, rc.CreateRemoteIn{
		Name:       name,
		Type:       provider,
		Parameters: params,
		Opt:        opt,
	}); err != nil {
		// Re-authenticating an existing remote: update it in place.
		if _, updateErr := c.UpdateRemote(ctx, name, params, opt); updateErr != nil {
			return friendlyConfigError(err)
		}
	}

	// rclone stores whatever it is told, even a config that fails on first use.
	// Check the remote once so a broken sign-in surfaces here instead of as an
	// empty account card later.
	checkCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if _, aboutErr := c.About(checkCtx, name+":"); aboutErr != nil {
		if _, listErr := c.List(checkCtx, name+":", "", rc.ListOpt{}); listErr != nil {
			return friendlyConfigError(listErr)
		}
	}

	_ = s.store.Update(func(cur *dto.Settings) { cur.Onboarded = true })
	return nil
}

// friendlyConfigError turns rclone's raw config errors into something a user
// can act on. The original text is kept as the wrapping message so the log
// still carries the technical detail.
func friendlyConfigError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Auth state doesn't match"):
		return fmt.Errorf("this sign-in page had already been used, so the provider rejected it — please sign in again: %w", err)
	case strings.Contains(msg, "unable to get drive_id"):
		return fmt.Errorf("Microsoft did not report which drive to use — please sign in again: %w", err)
	}
	return err
}

// ---------------------------------------------------------------- files

func (s *Service) ListRoots() dto.RootList {
	return dto.RootList{Items: files.Roots()}
}

func (s *Service) ListLocal(path string) dto.Listing {
	if strings.TrimSpace(path) == "" {
		return dto.Listing{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "path is required"}}
	}
	listing, err := files.List(path)
	if err != nil {
		return dto.Listing{Path: path, Err: failFrom(err)}
	}
	return listing
}

// Measure walks a local path and returns a file count and byte total. It is
// bounded so a huge tree cannot freeze the UI.
func (s *Service) Measure(path string) dto.Measure {
	if strings.TrimSpace(path) == "" {
		return dto.Measure{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "path is required"}}
	}
	deadline := time.Now().Add(20 * time.Second)
	var files int
	var bytes int64
	estimated := false
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if files >= 200000 || time.Now().After(deadline) {
			estimated = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), "$") || d.Name() == "System Volume Information" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files++
		bytes += info.Size()
		return nil
	})
	if err != nil {
		return dto.Measure{Err: failFrom(err)}
	}
	return dto.Measure{Files: files, Bytes: bytes, Estimated: estimated}
}

func (s *Service) ListCloud(accountID, path string) dto.Listing {
	c, fail := s.client()
	if fail != nil {
		return dto.Listing{Path: path, Err: fail}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	remote := strings.Trim(path, "/")
	started := time.Now()
	s.log.Info("service: list cloud start", "account", accountID, "path", remote)
	var entries []rc.Entry
	var err error
	if s.listBreakerOpen(accountID) {
		// The daemon timed out recently, which on Drive means its pacer is
		// backing off; a fresh process resets that state and answers in about
		// a second. Skipping the doomed daemon call removes an eight-second
		// stall from every folder the user opens.
		s.log.Info("service: rc list skipped after recent timeout", "account", accountID, "path", remote)
	} else {
		entries, err = c.List(ctx, accountID+":", remote, rc.ListOpt{})
		if err != nil {
			if rc.IsNotFound(err) {
				// A missing folder is not a wedged pacer; the CLI would only
				// confirm the same 404.
				s.log.Info("service: cloud path not found", "account", accountID, "path", remote, "err", err)
				return dto.Listing{Path: path, Err: failFrom(err)}
			}
			s.markListFailure(accountID)
		} else {
			s.clearListFailure(accountID)
		}
	}
	if err != nil || s.listBreakerOpen(accountID) {
		s.log.Warn("service: rc list failed, trying CLI", "account", accountID, "path", remote, "err", err)
		if cliEntries, cliErr := s.listViaCLI(accountID, remote); cliErr == nil {
			entries = cliEntries
			err = nil
			s.log.Info("service: cloud listed via CLI", "account", accountID, "path", remote, "entries", len(cliEntries), "took", time.Since(started).String())
		} else {
			s.log.Warn("service: CLI list failed", "err", cliErr)
			if err == nil {
				err = cliErr
			}
		}
	}
	if err != nil {
		s.log.Warn("service: list cloud failed", "account", accountID, "path", remote, "err", err)
		return dto.Listing{Path: path, Err: failFrom(err)}
	}
	s.log.Info("service: cloud listed", "account", accountID, "path", remote, "entries", len(entries), "took", time.Since(started).String())
	out := make([]dto.Entry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir && strings.HasSuffix(e.Name, ".cloudbox-part") {
			out = append(out, dto.Entry{
				Name:    strings.TrimSuffix(e.Name, ".cloudbox-part"),
				Path:    e.Path,
				IsDir:   false,
				Size:    0,
				ModTime: e.ModTime.UnixNano(),
				Split: &dto.SplitRef{
					State:        "incomplete",
					OriginalName: strings.TrimSuffix(e.Name, ".cloudbox-part"),
				},
			})
			continue
		}
		out = append(out, dto.Entry{
			Name:    e.Name,
			Path:    e.Path,
			Mime:    e.MimeType,
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: e.ModTime.UnixNano(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return dto.Listing{Path: path, Entries: out}
}

func (s *Service) CreateFolder(accountID, path, name string) dto.Empty {
	c, fail := s.client()
	if fail != nil {
		return dto.Empty{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	remote := strings.Trim(strings.Join([]string{path, name}, "/"), "/")
	_ = c.Mkdir(ctx, accountID+":", remote)
	return dto.Empty{}
}

// ---------------------------------------------------------------- transfers

func (s *Service) StartBackup(req dto.PlanRequest) dto.JobRef {
	c, fail := s.client()
	if fail != nil {
		return dto.JobRef{Err: fail}
	}
	if len(req.SourcePaths) == 0 {
		return dto.JobRef{Err: &dto.Fail{Code: string(apperr.NotFound), Message: "no source selected"}}
	}
	dest := destRootOf(req.DestRoot)
	kind := "sync/copy"
	if req.Mode == "mirror" {
		kind = "sync/sync"
	}
	transfers := req.Transfers
	if transfers <= 0 {
		transfers = s.store.Get().Backup.Transfers
	}

	id := fmt.Sprintf("job-%d", time.Now().UnixNano())
	group := "gui-" + id
	partSizeMB := s.store.Get().Backup.PartSizeMB
	job := &jobState{
		ID: id, Group: group, Kind: "backup", State: "running",
		StartedAt: time.Now(), DestRoot: dest, AccountID: req.AccountID,
		Sources: req.SourcePaths,
		// Remember the split intent so a later resume rebuilds the same plan
		// instead of syncing the large files whole.
		FastFiles:  req.FastFiles,
		Mode:       req.Mode,
		Excludes:   append([]string(nil), req.Excludes...),
		PartSizeMB: partSizeMB,
	}

	// Large-file acceleration is additive-only: a mirror sync would delete the
	// remote .cloudbox-part folders because they never exist in the source.
	splitOpts := splitOptionsFor(req.Mode, partSizeMB)
	var plan *splitBackupPlan
	if req.FastFiles && splitOpts.Enabled {
		p, err := buildSplitPlan(req.SourcePaths, dest, splitOpts, req.Excludes)
		if err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		plan = p
	}
	planActive := plan != nil && len(plan.files) > 0

	// Stat every source before starting any rclone job. A later missing source
	// must not leave the jobs already started by this call running without a
	// registered job, which would make them invisible to ActiveJobs/PauseJob.
	type backupSource struct {
		path  string
		base  string
		isDir bool
	}
	sources := make([]backupSource, 0, len(req.SourcePaths))
	for _, src := range req.SourcePaths {
		st, err := os.Stat(src)
		if err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		// A drive root has no folder name to sync into, so a mirror job would
		// target a malformed remote path. The wizard already blocks this;
		// refuse here too so the plan and the sync cannot diverge.
		base := filepath.Base(filepath.Clean(src))
		if req.Mode == "mirror" && st.IsDir() && !plainRemoteName(base) {
			return dto.JobRef{Err: &dto.Fail{
				Code:    "provider_unsupported",
				Message: "mirror needs a folder, not a drive root",
			}}
		}
		sources = append(sources, backupSource{
			path:  src,
			base:  base,
			isDir: st.IsDir(),
		})
	}

	for i, src := range sources {
		dstFs := req.AccountID + ":" + dest
		if src.isDir {
			dstFs = req.AccountID + ":" + dest + "/" + src.base
			job.Files = 0
		}
		job.Title = src.base

		if planActive {
			sp := &plan.sources[i]
			if len(sp.large) > 0 {
				// Small files travel in one filtered sync/copy per source; the
				// large ones are hashed, staged and uploaded by the goroutine
				// started after the job is registered.
				if sp.copySmall {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					smallJobID, err := startSmallFileSync(ctx, c, src.path, dstFs, group, transfers, splitOpts.Threshold, req.Excludes)
					cancel()
					if err != nil {
						s.abortBackup(c, group, job.JobIDs)
						return dto.JobRef{Err: failFrom(err)}
					}
					job.JobIDs = append(job.JobIDs, smallJobID)
				}
				continue
			}
		}

		if !src.isDir {
			// rclone's sync/copy refuses a file as srcFs ("is a file not a
			// directory"), which used to fail every single-file backup the
			// moment it started. copyfile moves exactly one file and keeps
			// its name.
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			copyJobID, err := c.StartCopyFile(ctx, rc.CopyFileIn{
				SrcFs:     filepath.Dir(src.path),
				SrcRemote: src.base,
				DstFs:     req.AccountID + ":" + dest,
				DstRemote: src.base,
				Group:     group,
			})
			cancel()
			if err != nil {
				s.abortBackup(c, group, job.JobIDs)
				return dto.JobRef{Err: failFrom(err)}
			}
			job.JobIDs = append(job.JobIDs, copyJobID)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		jobID, err := c.StartSync(ctx, kind, rc.SyncIn{
			SrcFs: src.path, DstFs: dstFs, Group: group, Transfers: transfers,
			Exclude: planner.RcloneRules(req.Excludes),
			Retries: s.retries(),
		})
		cancel()
		if err != nil {
			s.abortBackup(c, group, job.JobIDs)
			return dto.JobRef{Err: failFrom(err)}
		}
		job.JobIDs = append(job.JobIDs, jobID)
	}

	if planActive {
		job.SplitActive = true
		// runSplitBackup is launched below; mark the liveness flag before the
		// job becomes visible so a scheduler overlap check cannot mistake the
		// hashing/staging phase (no real RC job yet) for a finished backup.
		job.SplitRunning = true
		job.Phase = "hashing"
		job.TotalBytes = plan.totalBytes()
		// See pendingSplitJobID: keeps ActiveJobs from completing the job while
		// hashing/staging has no real RC job to poll.
		job.JobIDs = append(job.JobIDs, pendingSplitJobID)
	}

	s.mu.Lock()
	s.jobs[id] = job
	s.order = append(s.order, id)
	s.mu.Unlock()

	s.journalStart(dto.Record{
		ID: id, Kind: "backup", State: "running", Title: job.Title,
		StartedAt: job.StartedAt.Unix(), Files: job.Files,
	}, req)

	if planActive {
		go s.runSplitBackup(c, job, plan)
	}

	first := int64(0)
	for _, jobID := range job.JobIDs {
		if jobID > 0 {
			first = jobID
			break
		}
	}
	return dto.JobRef{ID: id, JobID: first, Group: group}
}

// abortBackup stops the rclone jobs already started for a backup which cannot
// finish starting. Such a job is not in s.jobs yet, so ActiveJobs, PauseJob and
// CancelJob cannot see or stop it; leaving it running would be invisible.
func (s *Service) abortBackup(c *rc.Client, group string, jobIDs []int64) {
	if len(jobIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.JobStopGroup(ctx, group); err != nil && s.log != nil {
		s.log.Warn("backup: stop orphaned jobs failed", "group", group, "err", err)
	}
}

func (s *Service) ActiveJobs() dto.JobProgressList {
	c, fail := s.client()
	if fail != nil {
		return dto.JobProgressList{Err: fail}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	s.mu.Lock()
	active := make([]*jobState, 0, len(s.jobs))
	for _, j := range s.jobs {
		active = append(active, j)
	}
	s.mu.Unlock()

	out := make([]dto.JobProgress, 0, len(active))
	epoch := s.engineEpoch()
	for _, j := range active {
		// Lock discipline: core/stats and job/status are network calls which
		// can block for seconds, so they must run without s.mu. Everything
		// they need is copied out in one short critical section; the updates
		// and the progress entry are then applied in a second short critical
		// section. The split goroutine mutates the same jobState fields under
		// s.mu, so no job field may be read or written outside a critical
		// section.
		s.mu.Lock()
		group, kind, phase, state := j.Group, j.Kind, j.Phase, j.State
		ids := append([]int64(nil), j.JobIDs...)
		startedAt, splitRunning, resuming := j.StartedAt, j.SplitRunning, j.Resuming
		s.mu.Unlock()
		if state == "done" || state == "cancelled" || state == "failed" || state == "interrupted" {
			continue
		}

		var stats *rc.Stats
		if phase != "verifying" && phase != "merging" {
			if st, err := c.Stats(ctx, group); err == nil && st != nil {
				stats = st
			}
		}

		finished := true
		success := true
		unobserved := false
		jobErr := ""
		if kind != "restore" {
			// A split job is owned by its goroutine, which records the real
			// outcome; ActiveJobs must not terminalise it while it hashes or
			// stages (its only id is the pending marker until parts start).
			// A job which ResumeJob is reserving is likewise in flight: its ids
			// still belong to the stopped run being replaced, so any probe answer
			// would be about the wrong run.
			if splitRunning || resuming {
				finished = false
			} else {
				// rclone purges finished jobs after about a minute, and it purges
				// successful and failed ones alike. An id which is unknown to the
				// engine therefore only proves the run ended, not that it
				// succeeded, so it is recorded as "interrupted" rather than done.
				// A probe which could not answer at all keeps it running.
				verdict, msg := probeJobLiveness(c, ctx, group, ids, startedAt, epoch, resuming)
				switch verdict {
				case verdictLive, verdictUnknown:
					finished = false
				case verdictFailed:
					success = false
					jobErr = msg
				case verdictUnobserved:
					unobserved = true
					jobErr = msg
				}
			}
		}

		s.mu.Lock()
		applied := false
		if stats != nil {
			j.Bytes = stats.Bytes
			// rclone never reports a "total files" which is meaningful for a
			// sync (only the transfers which have started), and Cloudnest used
			// to record 0 for every upload/download. Transfers plus checks is
			// the number of files the run has processed, which is what the
			// history row shows; a plan-derived count is never reduced.
			if files := int(stats.Transfers + stats.Checks); files > j.Files {
				j.Files = files
			}
			// Split backups know their plan total up front. rclone's own
			// totalBytes only counts transfers that have started, and the new
			// split pipeline starts parts a few at a time, so letting it win
			// would make the progress ring jump backwards as more parts start.
			if j.TotalBytes <= 0 && stats.TotalBytes > 0 {
				j.TotalBytes = stats.TotalBytes
			}
			j.Speed = stats.Speed
			if stats.ETA != nil {
				eta := int64(*stats.ETA)
				j.ETA = &eta
			}
			if label := transferringLabel(stats.Transferring); label != "" {
				j.Current = label
			}
			j.Failed = int(stats.Errors)
		}
		// Only apply the terminal state when the job is still the run which was
		// snapshotted above. PauseJob/CancelJob, a split goroutine which started
		// meanwhile, a resume which took its reservation, or a resume which
		// replaced the ids may have written a newer state during the network
		// calls; that state must win, and no History record may be written from
		// here.
		if kind != "restore" && finished && state == "running" &&
			j.State == "running" && !j.SplitRunning && !j.Resuming &&
			sameJobIDs(j.JobIDs, ids) {
			switch {
			case unobserved:
				j.State = "interrupted"
				j.Err = jobErr
			case !success:
				j.State = "failed"
				j.Err = jobErr
			default:
				j.State = "done"
			}
			applied = true
		}
		if j.State == "paused" {
			j.Speed = 0
		}
		progress := dto.JobProgress{
			ID: j.ID, Kind: j.Kind, State: j.State, Title: j.Title,
			Current: j.Current, Bytes: j.Bytes, TotalBytes: j.TotalBytes,
			Speed: j.Speed, ETA: j.ETA, TotalFiles: j.Files, Failed: j.Failed,
			Phase: j.Phase, Error: j.Err,
		}
		s.mu.Unlock()

		// Persisting the history record takes s.mu again, so it runs after the
		// critical section above. A transition that lost the race records
		// nothing.
		if applied {
			s.finishJob(j)
		}
		out = append(out, progress)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return dto.JobProgressList{Items: out}
}

// transferringLabel summarises what is moving right now. Several transfers can
// run at once (parallel split parts, or the user's "files at once" setting), so
// the remaining names are collapsed into a count instead of implying that only
// one file is in flight.
func transferringLabel(items []rc.Transferring) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Name != "" {
			names = append(names, item.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	if len(names) == 1 {
		return names[0]
	}
	return fmt.Sprintf("%s +%d", names[0], len(names)-1)
}

func (s *Service) PauseJob(id string) dto.Empty {
	c, fail := s.client()
	if fail != nil {
		return dto.Empty{}
	}
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		return dto.Empty{}
	}
	// Record the stop intent before asking rclone. ResumeJob reserves the job by
	// taking this snapshot, so a resume which is still starting new jobs will
	// see the change and stop them instead of committing underneath this stop.
	j.StopSeq++
	group := j.Group
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = c.JobStopGroup(ctx, group)
	// The rclone call runs without s.mu; only the shared state write is locked
	// so ActiveJobs and the scheduler's overlap guard see a consistent state.
	s.mu.Lock()
	j.State = "paused"
	s.mu.Unlock()
	return dto.Empty{}
}

func (s *Service) CancelJob(id string) dto.Empty {
	c, fail := s.client()
	if fail != nil {
		return dto.Empty{}
	}
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		return dto.Empty{}
	}
	// See PauseJob: bump the stop sequence before rclone stops the group so a
	// resume which started jobs after that snapshot aborts instead of
	// committing a transfer this cancel can no longer see.
	j.StopSeq++
	group := j.Group
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = c.JobStopGroup(ctx, group)
	s.mu.Lock()
	j.State = "cancelled"
	s.mu.Unlock()
	s.finishJob(j)
	return dto.Empty{}
}

func (s *Service) History(limit int) dto.RecordList {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.records)
	if n > limit {
		n = limit
	}
	return dto.RecordList{Items: append([]dto.Record(nil), s.records[:n]...)}
}

func (s *Service) finishJob(j *jobState) {
	s.mu.Lock()
	state := j.State
	if state == "running" {
		state = "done"
	}
	rec := dto.Record{
		ID: j.ID, Kind: j.Kind, State: state, Title: j.Title,
		StartedAt: j.StartedAt.Unix(), FinishedAt: time.Now().Unix(),
		Bytes: j.Bytes, Files: j.Files, Failed: j.Failed, Error: j.Err,
	}
	// A retried job reuses its id (ResumeJob resumes the same spec), so the
	// previous attempt's record must be replaced by this attempt's outcome, not
	// treated as a duplicate to drop. Keeping one row per job also keeps
	// Activity and the journal in agreement; the newest outcome moves to the
	// top like any other finished run.
	kept := make([]dto.Record, 0, len(s.records))
	for _, old := range s.records {
		if old.ID == j.ID {
			continue
		}
		kept = append(kept, old)
	}
	s.records = append([]dto.Record{rec}, kept...)
	if len(s.records) > 50 {
		s.records = s.records[:50]
	}
	s.mu.Unlock()
	// Persisted outside the lock: journalFinish takes it again.
	s.journalFinish(rec)
}

// markJobTerminal applies a terminal state to the job run the caller observed.
// It reports whether the transition was applied; a concurrent pause/cancel, or
// a resume which replaced the rclone ids, wins and leaves the state alone.
// finishJob runs after the lock is released because it takes s.mu itself.
func (s *Service) markJobTerminal(id, state, errMsg string, expectedIDs []int64) bool {
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil || j.State != "running" || j.SplitRunning || j.Resuming || !sameJobIDs(j.JobIDs, expectedIDs) {
		s.mu.Unlock()
		return false
	}
	j.State = state
	if (state == "failed" || state == "interrupted") && errMsg != "" {
		j.Err = errMsg
	}
	s.mu.Unlock()
	s.finishJob(j)
	return true
}

func sameJobIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reconcileAfterRestart terminalises jobs which were running in the old rclone
// process. The new process knows none of their ids, so those transfers can
// never finish, and leaving them "running" would wedge the scheduler's overlap
// guard and the window-close check. Every non-goroutine-owned kind is covered,
// but the engine epoch distinguishes old jobs from ones started against the new
// process so a fresh transfer is never failed by a restart.
func (s *Service) reconcileAfterRestart(ctx context.Context) {
	c, fail := s.client()
	if fail != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	epoch := s.engineEpoch()
	type staleJob struct {
		id        string
		group     string
		ids       []int64
		startedAt time.Time
	}
	s.mu.Lock()
	var jobs []staleJob
	for _, j := range s.jobs {
		// Split and restore jobs are owned by their goroutine, which records
		// its own outcome, and a job which ResumeJob is reserving still carries
		// the stopped run's ids, so they are left alone.
		if j.State != "running" || j.SplitRunning || j.SplitActive || j.Resuming || j.Kind == "restore" {
			continue
		}
		jobs = append(jobs, staleJob{
			id: j.ID, group: j.Group,
			ids: append([]int64(nil), j.JobIDs...), startedAt: j.StartedAt,
		})
	}
	s.mu.Unlock()

	for _, j := range jobs {
		verdict, msg := probeJobLiveness(c, ctx, j.group, j.ids, j.startedAt, epoch, false)
		switch verdict {
		case verdictLive, verdictUnknown:
			// Still running, or the engine could not answer. Do not fail it
			// from a single ambiguous probe.
			continue
		case verdictFailed:
			s.markJobTerminal(j.id, "failed", msg, j.ids)
		case verdictUnobserved:
			// The ids are gone, which proves the run ended but not how. A job
			// from an earlier engine may well have completed before the restart,
			// so it is recorded as interrupted (still retryable), never failed.
			s.markJobTerminal(j.id, "interrupted", msg, j.ids)
		default:
			// Definitive success reported by job/status.
			s.markJobTerminal(j.id, "done", "", j.ids)
		}
	}
}

// engineEpoch returns the time the current rclone process became ready. A job
// whose StartedAt is before it was started against an earlier process, so ids
// which the engine no longer knows were lost by a restart rather than expired
// after finishing. The zero value means the epoch is unknown (engine not ready
// or a test double without status); callers must then assume the same engine
// and never fail a job because an id is missing.
func (s *Service) engineEpoch() time.Time {
	if s == nil || s.eng == nil {
		return time.Time{}
	}
	st := s.eng.Status()
	if st.State != engine.StateReady || st.Since.IsZero() {
		return time.Time{}
	}
	return st.Since
}

// jobVerdict is the outcome of probing the rclone jobs behind one GUI job.
type jobVerdict int

const (
	// verdictFinished means every rclone job reported a successful finish.
	verdictFinished jobVerdict = iota
	// verdictLive means at least one rclone job is still running.
	verdictLive
	// verdictUnknown means at least one probe could not answer (timeout,
	// transport error). Liveness is unknown and must be treated as busy.
	verdictUnknown
	// verdictFailed means rclone definitively reported a failed job.
	verdictFailed
	// verdictUnobserved means the run ended but its outcome was not recorded:
	// every id is unknown to the engine (purged after finishing, recycled, or
	// lost with an earlier engine process). rclone purges successful and failed
	// jobs alike, so this is neither success nor a proven failure.
	verdictUnobserved
)

// probeJobLiveness classifies the rclone jobs behind one GUI job without ever
// turning an ambiguous answer into success or failure. A live or unanswerable
// probe wins over an expired id, so a timeout can never let a second transfer
// start. resuming short-circuits the whole probe: while ResumeJob has reserved
// the job its ids still belong to the stopped run being replaced, so the answer
// says nothing about the run in progress.
func probeJobLiveness(c *rc.Client, ctx context.Context, group string, ids []int64, startedAt, epoch time.Time, resuming bool) (jobVerdict, string) {
	if resuming {
		return verdictLive, ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	real := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			real = append(real, id)
		}
	}
	sameEngine := epoch.IsZero() || startedAt.IsZero() || !startedAt.Before(epoch)
	if len(real) == 0 {
		// No rclone job was ever recorded. That is not evidence of success.
		if sameEngine {
			return verdictUnobserved, "rclone no longer reports this job; its outcome was not recorded"
		}
		return verdictUnobserved, "rclone restarted before this job's result was recorded; it may have completed"
	}

	live, unknown, gone, failed := false, false, false, false
	errMsg := ""
	for _, id := range real {
		qctx, cancel := context.WithTimeout(ctx, schedulerProbeTimeout)
		st, err := c.JobStatus(qctx, id)
		cancel()
		if err != nil {
			if rc.IsNotFound(err) {
				// rclone no longer knows this id. It purges finished jobs
				// whether they succeeded or failed, so this only proves the run
				// ended; the outcome is classified below.
				gone = true
				continue
			}
			// Timeout or a transport/internal error says nothing about
			// liveness; never treat it as gone.
			unknown = true
			continue
		}
		if st.Group != "" && group != "" && st.Group != group {
			// The numeric id was recycled by a newer job; the original is gone.
			gone = true
			continue
		}
		if !st.Finished {
			live = true
			continue
		}
		if !st.Success {
			failed = true
			if errMsg == "" {
				errMsg = st.Error
			}
		}
	}

	switch {
	case live:
		return verdictLive, ""
	case unknown:
		return verdictUnknown, ""
	case failed:
		if errMsg == "" {
			errMsg = "job did not finish"
		}
		return verdictFailed, errMsg
	case gone && !sameEngine:
		return verdictUnobserved, "rclone restarted before this job's result was recorded; it may have completed"
	case gone:
		// Same engine, id purged. rclone expires both successful and failed
		// jobs, so the outcome was simply not observed.
		return verdictUnobserved, "rclone no longer reports this job; its outcome was not recorded"
	default:
		return verdictFinished, ""
	}
}

// recordRestartFailure writes a visible History entry when an engine restart
// could not complete. Settings such as the proxy or verbose logging only take
// effect after a restart, so a silent failure would leave a saved value
// inactive with no indication. The app event lets a future frontend toast it.
func (s *Service) recordRestartFailure(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	rec := dto.Record{
		ID:   fmt.Sprintf("restart-%d", time.Now().UnixNano()),
		Kind: "settings", State: "failed", Title: "Engine restart",
		StartedAt: time.Now().Unix(), FinishedAt: time.Now().Unix(), Error: msg,
	}
	s.mu.Lock()
	s.records = append([]dto.Record{rec}, s.records...)
	if len(s.records) > 50 {
		s.records = s.records[:50]
	}
	s.mu.Unlock()
	if s.app != nil {
		s.app.Event.Emit("cloudnest:restart-failed", map[string]any{"message": msg})
	}
}

// ---------------------------------------------------------------- helpers

func (s *Service) client() (*rc.Client, *dto.Fail) {
	c, err := s.eng.Client()
	if err != nil {
		return nil, &dto.Fail{Code: string(apperr.EngineNotReady), Message: "cloud engine is not ready", Retry: true}
	}
	return c, nil
}

// remoteConfig reads a remote's provider id and reports whether a Google Drive
// remote has no client_id of its own. rclone's shared Drive client_id is being
// retired and is throttled hard, which is the usual cause of a listing or quota
// read taking tens of seconds; the UI uses this to point the user at a fix.
func (s *Service) remoteConfig(ctx context.Context, c *rc.Client, name string) (provider string, sharedClientID bool) {
	// Decoding only these keys keeps the OAuth token, which config/get also
	// returns, out of memory and out of any log line.
	var out struct {
		Type     string `json:"type"`
		ClientID string `json:"client_id"`
	}
	if err := c.Call(ctx, "config/get", map[string]any{"name": name}, &out); err != nil {
		return "webdav", false
	}
	provider = providerID(out.Type)
	sharedClientID = out.Type == "drive" && strings.TrimSpace(out.ClientID) == ""
	return provider, sharedClientID
}

func (s *Service) uniqueRemoteName(base string) string {
	c, err := s.eng.Client()
	if err != nil {
		return base
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	remotes, err := c.ListRemotes(ctx, false)
	if err != nil {
		return base
	}
	taken := make(map[string]bool, len(remotes))
	for _, r := range remotes {
		taken[r.Name] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s %d", base, i)
		if !taken[cand] {
			return cand
		}
	}
	return base
}

func mapStatus(st engine.Status) dto.EngineStatus {
	out := dto.EngineStatus{State: string(st.State), Version: st.Version, Port: st.Port, ConfigPath: st.ConfigPath}
	if st.Err != nil {
		out.Err = failFrom(st.Err)
	}
	return out
}

func failFrom(err error) *dto.Fail {
	if err == nil {
		return nil
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return &dto.Fail{Code: string(ae.Code), Message: ae.Message, Detail: ae.Detail, Retry: ae.Retry}
	}
	var re *rc.Error
	if errors.As(err, &re) {
		e := apperr.Classify(re.Status, re.Message)
		return &dto.Fail{Code: string(e.Code), Message: e.Message, Detail: e.Detail, Retry: e.Retry}
	}
	if errors.Is(err, os.ErrNotExist) {
		return &dto.Fail{Code: string(apperr.NotFound), Message: "not found"}
	}
	return &dto.Fail{Code: string(apperr.Unknown), Message: "unexpected error", Detail: err.Error(), Retry: true}
}

func providerID(t string) string {
	switch strings.ToLower(t) {
	case "drive":
		return "google"
	case "onedrive":
		return "onedrive"
	case "dropbox":
		return "dropbox"
	case "box":
		return "box"
	case "mega":
		return "mega"
	case "pcloud":
		return "pcloud"
	case "s3", "b2", "azureblob", "gcs", "swift":
		return "aws"
	default:
		return "webdav"
	}
}

func rcloneType(provider string) string {
	switch provider {
	case "google":
		return "drive"
	case "aws":
		return "s3"
	default:
		return provider
	}
}

func defaultRemoteName(provider string) string {
	switch provider {
	case "google":
		return "Google Drive"
	case "onedrive":
		return "OneDrive"
	case "dropbox":
		return "Dropbox"
	case "box":
		return "Box"
	case "mega":
		return "MEGA"
	case "pcloud":
		return "pCloud"
	case "aws":
		return "S3"
	default:
		return "Cloud"
	}
}

// sameStrings reports whether two string slices are identical in order. Used
// to decide whether a saved schedule still refers to the same work.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
