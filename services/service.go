// Package services is the only Wails-bound layer. Every exported method is
// callable from the Svelte frontend and returns a DTO with an Err field for
// expected failures.
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"cloudnest/internal/apperr"
	"cloudnest/internal/dto"
	"cloudnest/internal/engine"
	"cloudnest/internal/files"
	"cloudnest/internal/oauth"
	"cloudnest/internal/platform"
	"cloudnest/internal/rc"
	"cloudnest/internal/settings"
)

const Version = "0.1.0"

type jobState struct {
	ID         string
	Group      string
	JobIDs     []int64
	Kind       string
	Title      string
	StartedAt  time.Time
	TotalBytes int64
	Files      int
	State      string
	Phase      string
	Bytes      int64
	Speed      float64
	ETA        *int64
	Current    string
	Failed     int
	Sources    []string
	DestRoot   string
	AccountID  string
}

type usageEntry struct {
	used  int64
	total int64
	free  int64
	at    time.Time
}

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
}

func New(app *application.App, eng engine.Engine, store *settings.Store, rclonePath string, logger *slog.Logger) *Service {
	dataDir, _ := platform.DataDir()
	s := &Service{
		app:      app,
		eng:      eng,
		store:    store,
		rclone:   rclonePath,
		log:      logger,
		dataDir:  dataDir,
		portable: dataDir != "",
		jobs:     make(map[string]*jobState),
		usage:    make(map[string]usageEntry),
	}
	s.oauth = oauth.New(rclonePath, s.persistToken)
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
	return out
}

func (s *Service) EngineStatus() dto.EngineStatus { return mapStatus(s.eng.Status()) }

func (s *Service) RestartEngine() dto.Empty {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.eng.Restart(ctx); err != nil {
		return dto.Empty{}
	}
	return dto.Empty{}
}

func (s *Service) GetSettings() dto.Settings { return s.store.Get() }

func (s *Service) SaveSettings(in dto.Settings) dto.Empty {
	before := s.store.Get().Advanced
	_ = s.store.Update(func(cur *dto.Settings) {
		if in.Language != "" {
			cur.Language = in.Language
		}
		if in.Theme != "" {
			cur.Theme = in.Theme
		}
		cur.Backup = in.Backup
		cur.Advanced = in.Advanced
	})
	after := s.store.Get().Advanced
	// Proxy and verbose logging live in rclone's environment, so a change can
	// only take effect by relaunching it. Restart in the background so the
	// settings dialog stays responsive.
	if after.Proxy != before.Proxy || after.Verbose != before.Verbose {
		s.eng.ApplyRuntime(after.Verbose, after.Proxy)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.eng.Restart(ctx); err != nil && s.log != nil {
				s.log.Warn("restart after settings change", "err", err)
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
		provider := s.remoteProvider(typeCtx, c, r.Name)
		typeCancel()
		out[i] = dto.Account{ID: r.Name, Provider: provider, Name: r.Name, Status: "connected"}
		if used, total, ok := s.cachedUsage(r.Name); ok {
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
	c, fail := s.client()
	if fail != nil {
		return dto.Usage{Err: fail}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u, err := c.About(ctx, id+":")
	if err != nil {
		return dto.Usage{Err: failFrom(err)}
	}
	s.storeUsage(id, u.Used, u.Total, u.Free)
	return dto.Usage{Used: u.Used, Total: u.Total, Free: u.Free}
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

func (s *Service) storeUsage(id string, used, total, free int64) {
	s.mu.Lock()
	s.usage[id] = usageEntry{used: used, total: total, free: free, at: time.Now()}
	s.mu.Unlock()
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
	if displayName == "" {
		displayName = defaultRemoteName(provider)
	}
	name := s.uniqueRemoteName(displayName)
	id, err := s.oauth.Begin(typ, name)
	if err != nil {
		if errors.Is(err, oauth.ErrBusy) {
			return dto.ConnectSession{Err: &dto.Fail{Code: string(apperr.OAuthBusy), Message: "another sign-in is running"}}
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
	if _, err := c.CreateRemote(ctx, rc.CreateRemoteIn{
		Name:       name,
		Type:       provider,
		Parameters: params,
	}); err != nil {
		// Re-authenticating an existing remote: update it in place.
		if _, updateErr := c.UpdateRemote(ctx, name, params, nil); updateErr != nil {
			return err
		}
	}
	_ = s.store.Update(func(cur *dto.Settings) { cur.Onboarded = true })
	return nil
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
	entries, err := c.List(ctx, accountID+":", remote, rc.ListOpt{})
	if err != nil {
		s.log.Warn("service: rc list failed, trying CLI", "account", accountID, "path", remote, "err", err)
		if cliEntries, cliErr := s.listViaCLI(accountID, remote); cliErr == nil {
			entries = cliEntries
			err = nil
			s.log.Info("service: cloud listed via CLI", "account", accountID, "path", remote, "entries", len(cliEntries), "took", time.Since(started).String())
		} else {
			s.log.Warn("service: CLI list failed", "err", cliErr)
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
	dest := strings.Trim(req.DestRoot, "/ ")
	if dest == "" {
		dest = "電腦備份"
	}
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
	job := &jobState{
		ID: id, Group: group, Kind: "backup", State: "running",
		StartedAt: time.Now(), DestRoot: dest, AccountID: req.AccountID,
		Sources: req.SourcePaths,
	}

	// Large-file acceleration is additive-only: a mirror sync would delete the
	// remote .cloudbox-part folders because they never exist in the source.
	splitOpts := splitOptionsFor(req.Mode, s.store.Get().Backup.PartSizeMB)
	var plan *splitBackupPlan
	if req.FastFiles && splitOpts.Enabled {
		p, err := buildSplitPlan(req.SourcePaths, dest, splitOpts, req.Excludes)
		if err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		plan = p
	}
	planActive := plan != nil && len(plan.files) > 0

	for i, src := range req.SourcePaths {
		st, err := os.Stat(src)
		if err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		base := filepath.Base(filepath.Clean(src))
		dstFs := req.AccountID + ":" + dest
		if st.IsDir() {
			dstFs = req.AccountID + ":" + dest + "/" + base
			job.Files = 0
		}
		job.Title = base

		if planActive {
			sp := &plan.sources[i]
			if len(sp.large) > 0 {
				// Small files travel in one filtered sync/copy per source; the
				// large ones are hashed, staged and uploaded by the goroutine
				// started after the job is registered.
				if sp.copySmall {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					smallJobID, err := startSmallFileSync(ctx, c, src, dstFs, group, transfers, splitOpts.Threshold, req.Excludes)
					cancel()
					if err != nil {
						return dto.JobRef{Err: failFrom(err)}
					}
					job.JobIDs = append(job.JobIDs, smallJobID)
				}
				continue
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		jobID, err := c.StartSync(ctx, kind, rc.SyncIn{
			SrcFs: src, DstFs: dstFs, Group: group, Transfers: transfers, Exclude: req.Excludes,
		})
		cancel()
		if err != nil {
			return dto.JobRef{Err: failFrom(err)}
		}
		job.JobIDs = append(job.JobIDs, jobID)
	}

	if planActive {
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
	for _, j := range active {
		if j.State == "done" || j.State == "cancelled" || j.State == "failed" {
			continue
		}
		if stats, err := c.Stats(ctx, j.Group); err == nil && stats != nil && j.Phase != "verifying" && j.Phase != "merging" {
			j.Bytes = stats.Bytes
			if stats.TotalBytes > 0 {
				j.TotalBytes = stats.TotalBytes
			}
			j.Speed = stats.Speed
			if stats.ETA != nil {
				eta := int64(*stats.ETA)
				j.ETA = &eta
			}
			if len(stats.Transferring) > 0 {
				j.Current = stats.Transferring[0].Name
			}
			j.Failed = int(stats.Errors)
		}
		finished := true
		success := true
		if j.Kind != "restore" {
			for _, id := range j.JobIDs {
				js, err := c.JobStatus(ctx, id)
				if err != nil {
					finished = false
					break
				}
				if !js.Finished {
					finished = false
					break
				}
				if !js.Success {
					success = false
				}
			}
			if finished {
				j.State = "done"
				if !success {
					j.State = "failed"
				}
				s.finishJob(j)
			}
		}
		if j.State == "paused" {
			j.Speed = 0
		}
		out = append(out, dto.JobProgress{
			ID: j.ID, Kind: j.Kind, State: j.State, Title: j.Title,
			Current: j.Current, Bytes: j.Bytes, TotalBytes: j.TotalBytes,
			Speed: j.Speed, ETA: j.ETA, TotalFiles: j.Files, Failed: j.Failed,
			Phase: j.Phase,
		})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return dto.JobProgressList{Items: out}
}

func (s *Service) PauseJob(id string) dto.Empty {
	c, fail := s.client()
	if fail != nil {
		return dto.Empty{}
	}
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		return dto.Empty{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = c.JobStopGroup(ctx, j.Group)
	j.State = "paused"
	return dto.Empty{}
}

func (s *Service) CancelJob(id string) dto.Empty {
	c, fail := s.client()
	if fail != nil {
		return dto.Empty{}
	}
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		return dto.Empty{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = c.JobStopGroup(ctx, j.Group)
	j.State = "cancelled"
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
	defer s.mu.Unlock()
	for _, rec := range s.records {
		if rec.ID == j.ID {
			return
		}
	}
	state := j.State
	if state == "running" {
		state = "done"
	}
	rec := dto.Record{
		ID: j.ID, Kind: j.Kind, State: state, Title: j.Title,
		StartedAt: j.StartedAt.Unix(), FinishedAt: time.Now().Unix(),
		Bytes: j.Bytes, Files: j.Files, Failed: j.Failed,
	}
	s.records = append([]dto.Record{rec}, s.records...)
	if len(s.records) > 50 {
		s.records = s.records[:50]
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

func (s *Service) remoteProvider(ctx context.Context, c *rc.Client, name string) string {
	var out map[string]any
	if err := c.Call(ctx, "config/get", map[string]any{"name": name}, &out); err == nil {
		if t, ok := out["type"].(string); ok {
			return providerID(t)
		}
	}
	return "webdav"
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
