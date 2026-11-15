package rc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// asyncControl is the set of rclone RC control fields prefixed with an
// underscore. It is embedded into request structs or passed to Call by
// helpers; exported request DTOs never carry these fields directly.
type asyncControl struct {
	Async  bool       `json:"_async,omitempty"`
	Group  string     `json:"_group,omitempty"`
	Config *jobConfig `json:"_config,omitempty"`
}

// jobConfig is the subset of rclone's per-job configuration Cloudnest uses.
type jobConfig struct {
	Transfers int    `json:"Transfers,omitempty"`
	Checkers  int    `json:"Checkers,omitempty"`
	DryRun    bool   `json:"DryRun"`
	BwLimit   string `json:"BwLimit,omitempty"`
	Retries   int    `json:"Retries,omitempty"`
}

func newAsyncControl(async bool, group string, cfg *jobConfig) asyncControl {
	if !async {
		return asyncControl{}
	}
	return asyncControl{Async: true, Group: group, Config: cfg}
}

// Version returns the rclone core/version response.
func (c *Client) Version(ctx context.Context) (*Version, error) {
	var out Version
	if err := c.Call(ctx, "core/version", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// cachedVersion returns the core/version response, probing rclone at most once
// per client. A failed probe is not cached so a later call can retry.
func (c *Client) cachedVersion(ctx context.Context) (*Version, error) {
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	if c.version != nil {
		return c.version, nil
	}
	v, err := c.Version(ctx)
	if err != nil {
		return nil, err
	}
	c.version = v
	return v, nil
}

// ListRemotes lists configured remote names. long is forwarded to rclone; on
// versions which ignore it the returned Remotes contain names only.
func (c *Client) ListRemotes(ctx context.Context, long bool) ([]Remote, error) {
	var resp struct {
		Remotes []json.RawMessage `json:"remotes"`
	}
	if err := c.Call(ctx, "config/listremotes", map[string]any{"long": long}, &resp); err != nil {
		return nil, err
	}
	remotes := make([]Remote, 0, len(resp.Remotes))
	for _, raw := range resp.Remotes {
		var remote Remote
		if err := json.Unmarshal(raw, &remote); err != nil {
			return nil, fmt.Errorf("rc: config/listremotes: decode remote: %w", err)
		}
		remotes = append(remotes, remote)
	}
	return remotes, nil
}

// CreateRemote creates an rclone remote with config/create.
//
// Set CreateRemoteIn.Async for the non-interactive OAuth flow: the request
// then runs as a job and the returned ConfigResult carries its JobID.
func (c *Client) CreateRemote(ctx context.Context, in CreateRemoteIn) (*ConfigResult, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("rc: CreateRemote: name is required")
	}
	if strings.TrimSpace(in.Type) == "" {
		return nil, errors.New("rc: CreateRemote: type is required")
	}
	group := in.Group
	if in.Async && group == "" {
		group = "cloudnest-config-" + randomID(8)
	}
	req := createRemoteRequest{
		CreateRemoteIn: in,
		asyncControl:   newAsyncControl(in.Async, group, nil),
	}
	var out ConfigResult
	if err := c.Call(ctx, "config/create", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type createRemoteRequest struct {
	CreateRemoteIn
	asyncControl
}

// StartCopyFile copies a single file with operations/copyfile and returns its
// job id. The call is always asynchronous so one job id represents the
// transfer, just like StartSync. It exists because sync/copy refuses a file as
// srcFs ("is a file not a directory"), which is exactly the single-file backup
// case.
func (c *Client) StartCopyFile(ctx context.Context, in CopyFileIn) (int64, error) {
	for name, value := range map[string]string{
		"srcFs": in.SrcFs, "srcRemote": in.SrcRemote,
		"dstFs": in.DstFs, "dstRemote": in.DstRemote,
	} {
		if strings.TrimSpace(value) == "" {
			return 0, fmt.Errorf("rc: CopyFile: %s is required", name)
		}
	}
	group := in.Group
	if group == "" {
		group = "cloudnest-copy-" + randomID(8)
	}
	req := copyFileRequest{
		SrcFs:        in.SrcFs,
		SrcRemote:    in.SrcRemote,
		DstFs:        in.DstFs,
		DstRemote:    in.DstRemote,
		asyncControl: newAsyncControl(true, group, nil),
	}
	var resp struct {
		JobID int64 `json:"jobid"`
	}
	if err := c.Call(ctx, "operations/copyfile", req, &resp); err != nil {
		return 0, err
	}
	if resp.JobID <= 0 {
		return 0, errors.New("rclone did not return a job id")
	}
	return resp.JobID, nil
}

type copyFileRequest struct {
	SrcFs     string `json:"srcFs"`
	SrcRemote string `json:"srcRemote"`
	DstFs     string `json:"dstFs"`
	DstRemote string `json:"dstRemote"`
	asyncControl
}

// UpdateRemote updates an existing remote with config/update.
func (c *Client) UpdateRemote(ctx context.Context, name string, parameters map[string]any, opt *ConfigOpt) (*ConfigResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("rc: UpdateRemote: name is required")
	}
	req := updateRemoteRequest{Name: name, Parameters: parameters, Opt: opt}
	var out ConfigResult
	if err := c.Call(ctx, "config/update", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type updateRemoteRequest struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Opt        *ConfigOpt     `json:"opt,omitempty"`
}

// DeleteRemote deletes a remote from the config with config/delete.
func (c *Client) DeleteRemote(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("rc: DeleteRemote: name is required")
	}
	return c.Call(ctx, "config/delete", map[string]any{"name": name}, nil)
}

// Providers returns the storage backends known to rclone.
func (c *Client) Providers(ctx context.Context) ([]Provider, error) {
	var resp struct {
		Providers []Provider `json:"providers"`
	}
	if err := c.Call(ctx, "config/providers", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Providers, nil
}

// OAuthStatus polls config/oauthstatus for an in-progress remote setup.
//
// rclone 1.70.3 does not expose this method (it replies with a not-found
// *Error). It is implemented here for the OAuth state machine and newer
// rclone builds which provide it.
func (c *Client) OAuthStatus(ctx context.Context, name string) (*OAuthStatus, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("rc: OAuthStatus: name is required")
	}
	var out OAuthStatus
	if err := c.Call(ctx, "config/oauthstatus", map[string]any{"name": name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OAuthStop cancels an in-progress OAuth flow. rclone 1.70.3 does not expose
// this method (it replies with a not-found *Error).
func (c *Client) OAuthStop(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("rc: OAuthStop: name is required")
	}
	return c.Call(ctx, "config/oauthstop", map[string]any{"name": name}, nil)
}

// List lists a directory with operations/list. remote is required by rclone
// even when listing the root of fs; pass an empty string for the root. opts
// configure the underlying Call; pass WithTimeout when a large subtree may
// need more than DefaultTimeout.
func (c *Client) List(ctx context.Context, fs, remote string, opt ListOpt, opts ...CallOption) ([]Entry, error) {
	req := listRequest{Fs: fs, Remote: remote, Opt: opt}
	var resp struct {
		List []Entry `json:"list"`
	}
	if err := c.Call(ctx, "operations/list", req, &resp, opts...); err != nil {
		return nil, err
	}
	return resp.List, nil
}

type listRequest struct {
	Fs     string  `json:"fs"`
	Remote string  `json:"remote"`
	Opt    ListOpt `json:"opt"`
}

// Stat returns metadata for one object. A nil Entry with a nil error means
// rclone returned item: null (the object does not exist).
func (c *Client) Stat(ctx context.Context, fs, remote string, opt ListOpt) (*Entry, error) {
	req := statRequest{Fs: fs, Remote: remote, Opt: opt}
	var resp struct {
		Item *Entry `json:"item"`
	}
	if err := c.Call(ctx, "operations/stat", req, &resp); err != nil {
		return nil, err
	}
	return resp.Item, nil
}

type statRequest struct {
	Fs     string  `json:"fs"`
	Remote string  `json:"remote"`
	Opt    ListOpt `json:"opt"`
}

// About returns free/used/total space for fs with operations/about.
func (c *Client) About(ctx context.Context, fs string) (*Usage, error) {
	var out Usage
	if err := c.Call(ctx, "operations/about", map[string]any{"fs": fs}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Mkdir creates a directory with operations/mkdir.
func (c *Client) Mkdir(ctx context.Context, fs, remote string) error {
	return c.Call(ctx, "operations/mkdir", pathRequest{Fs: fs, Remote: remote}, nil)
}

// DeleteFile deletes a single file with operations/deletefile.
func (c *Client) DeleteFile(ctx context.Context, fs, remote string) error {
	return c.Call(ctx, "operations/deletefile", pathRequest{Fs: fs, Remote: remote}, nil)
}

// Purge removes a directory and everything below it with operations/purge.
func (c *Client) Purge(ctx context.Context, fs, remote string) error {
	return c.Call(ctx, "operations/purge", pathRequest{Fs: fs, Remote: remote}, nil)
}

type pathRequest struct {
	Fs     string `json:"fs"`
	Remote string `json:"remote"`
}

// CopyFile copies one object with operations/copyfile.
func (c *Client) CopyFile(ctx context.Context, in CopyFileIn) error {
	return c.Call(ctx, "operations/copyfile", in, nil)
}

// StartSync starts an asynchronous copy or sync job and returns its job id.
//
// kind accepts "copy", "sync", "sync/copy" or "sync/sync". The request is
// always asynchronous and carries _group and _config; in.Group controls the
// group and is generated when empty. in.Transfers and in.Checkers are only
// added to _config when positive so rclone keeps its defaults; DryRun is
// always included. in.Exclude becomes a _filter group, omitted when empty.
func (c *Client) StartSync(ctx context.Context, kind string, in SyncIn) (int64, error) {
	method, err := syncMethod(kind)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(in.SrcFs) == "" || strings.TrimSpace(in.DstFs) == "" {
		return 0, errors.New("rc: StartSync: srcFs and dstFs are required")
	}
	group := in.Group
	if group == "" {
		group = "cloudnest-sync-" + randomID(8)
	}
	cfg := &jobConfig{DryRun: in.DryRun}
	if in.Transfers > 0 {
		cfg.Transfers = in.Transfers
	}
	if in.Checkers > 0 {
		cfg.Checkers = in.Checkers
	}
	if limit := strings.TrimSpace(in.BwLimit); limit != "" && !strings.EqualFold(limit, "off") {
		cfg.BwLimit = limit
	}
	if in.Retries > 0 {
		cfg.Retries = in.Retries
	}
	req := syncRequest{
		SrcFs:              in.SrcFs,
		DstFs:              in.DstFs,
		CreateEmptySrcDirs: in.CreateEmptySrcDirs,
		asyncControl:       newAsyncControl(true, group, cfg),
	}
	if len(in.Exclude) > 0 {
		// Keys are decoded by rclone into its filter Options struct by field
		// name, so the exclude rules must be ExcludeRule (config "exclude").
		// An "Exclude" key is silently ignored, which would make the sync
		// upload and delete files the caller meant to leave alone.
		req.Filter = &syncFilter{ExcludeRule: in.Exclude, IgnoreCase: true}
	}
	var resp struct {
		JobID int64 `json:"jobid"`
	}
	if err := c.Call(ctx, method, req, &resp); err != nil {
		return 0, err
	}
	if resp.JobID <= 0 {
		return 0, errors.New("rc: StartSync: server did not return a job id")
	}
	return resp.JobID, nil
}

type syncRequest struct {
	SrcFs              string      `json:"srcFs"`
	DstFs              string      `json:"dstFs"`
	CreateEmptySrcDirs bool        `json:"createEmptySrcDirs,omitempty"`
	Filter             *syncFilter `json:"_filter,omitempty"`
	asyncControl
}

// syncFilter is the subset of rclone's _filter group used by StartSync.
// rclone decodes the group into its filter Options struct by Go field name,
// which is why the exclude list is ExcludeRule (config name "exclude"), not
// "Exclude". IgnoreCase matches the planner's case-insensitive matching so the
// preflight and the sync exclude the same files.
type syncFilter struct {
	ExcludeRule []string `json:"ExcludeRule,omitempty"`
	IgnoreCase  bool     `json:"IgnoreCase,omitempty"`
}

func syncMethod(kind string) (string, error) {
	k := strings.TrimSpace(kind)
	k = strings.TrimPrefix(k, "sync/")
	switch k {
	case "copy":
		return "sync/copy", nil
	case "sync":
		return "sync/sync", nil
	default:
		return "", fmt.Errorf("rc: unknown sync kind %q (want \"copy\" or \"sync\")", kind)
	}
}

// JobStatus returns the status of one job.
func (c *Client) JobStatus(ctx context.Context, jobID int64) (*JobStatus, error) {
	if jobID <= 0 {
		return nil, fmt.Errorf("rc: JobStatus: invalid job id %d", jobID)
	}
	var out JobStatus
	if err := c.Call(ctx, "job/status", map[string]any{"jobid": jobID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// JobList lists currently known job ids.
func (c *Client) JobList(ctx context.Context) (*JobList, error) {
	var out JobList
	if err := c.Call(ctx, "job/list", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// JobStop stops one job.
func (c *Client) JobStop(ctx context.Context, jobID int64) error {
	if jobID <= 0 {
		return fmt.Errorf("rc: JobStop: invalid job id %d", jobID)
	}
	return c.Call(ctx, "job/stop", map[string]any{"jobid": jobID}, nil)
}

// JobStopGroup stops every job in a group.
func (c *Client) JobStopGroup(ctx context.Context, group string) error {
	if strings.TrimSpace(group) == "" {
		return errors.New("rc: JobStopGroup: group is required")
	}
	return c.Call(ctx, "job/stopgroup", map[string]any{"group": group}, nil)
}

// Stats returns core/stats for group. An empty group means all groups.
func (c *Client) Stats(ctx context.Context, group string) (*Stats, error) {
	var req any
	if group != "" {
		req = map[string]any{"group": group}
	}
	var out Stats
	if err := c.Call(ctx, "core/stats", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Transferred returns recently completed transfers (core/transferred). An
// empty group means all groups.
func (c *Client) Transferred(ctx context.Context, group string) ([]TransferredItem, error) {
	var req any
	if group != "" {
		req = map[string]any{"group": group}
	}
	var resp struct {
		Transferred []TransferredItem `json:"transferred"`
	}
	if err := c.Call(ctx, "core/transferred", req, &resp); err != nil {
		return nil, err
	}
	return resp.Transferred, nil
}

// Quit asks rclone to exit with core/quit.
func (c *Client) Quit(ctx context.Context) error {
	return c.Call(ctx, "core/quit", nil, nil)
}

// randomID returns n random bytes encoded with URL-safe base64. It is used
// for default job group names and is not a security primitive.
func randomID(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// MoveFile renames or moves one object with operations/movefile.
func (c *Client) MoveFile(ctx context.Context, in CopyFileIn) error {
	for name, value := range map[string]string{
		"srcFs": in.SrcFs, "srcRemote": in.SrcRemote,
		"dstFs": in.DstFs, "dstRemote": in.DstRemote,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("rc: MoveFile: %s is required", name)
		}
	}
	return c.Call(ctx, "operations/movefile", copyFileRequest{
		SrcFs: in.SrcFs, SrcRemote: in.SrcRemote,
		DstFs: in.DstFs, DstRemote: in.DstRemote,
	}, nil)
}

// hashSumFileMinVersion is the first rclone release that implements the
// operations/hashsumfile RC method. Cloudnest still supports rclone 1.70, where
// the method is absent and rclone answers with a "couldn't find method" 404, so
// the call is version-gated to produce an actionable error instead.
const (
	hashSumFileMinMajor = 1
	hashSumFileMinMinor = 73
	hashSumFileMinPatch = 0
)

// ErrHashSumFileUnsupported reports that the running rclone is too old for
// operations/hashsumfile. Callers can detect it with errors.Is.
var ErrHashSumFileUnsupported = errors.New("rc: operations/hashsumfile requires rclone v1.73.0 or newer")

// HashSumFile returns the hash of one object. hashType is an rclone hash name
// such as "sha256" or "md5". opts configure the underlying Call; pass
// WithTimeout when a large object may need more than DefaultTimeout.
func (c *Client) HashSumFile(ctx context.Context, fs, remote, hashType string, opts ...CallOption) (string, error) {
	if strings.TrimSpace(fs) == "" || strings.TrimSpace(remote) == "" {
		return "", errors.New("rc: HashSumFile: fs and remote are required")
	}
	if err := c.checkHashSumFile(ctx); err != nil {
		return "", err
	}
	var out struct {
		Hash     string `json:"hash"`
		HashType string `json:"hashType"`
	}
	req := map[string]string{"fs": fs, "remote": remote, "hashType": hashType}
	if err := c.Call(ctx, "operations/hashsumfile", req, &out, opts...); err != nil {
		return "", err
	}
	return out.Hash, nil
}

// checkHashSumFile rejects operations/hashsumfile on rclone builds older than
// 1.73. The version is probed once per client. When it cannot be determined the
// call is attempted rather than blocked, so an unrecognised build is not
// rejected by mistake.
func (c *Client) checkHashSumFile(ctx context.Context) error {
	v, err := c.cachedVersion(ctx)
	if err != nil {
		return fmt.Errorf("rc: HashSumFile: cannot determine rclone version: %w", err)
	}
	major, minor, patch, ok := versionNumbers(v)
	if !ok {
		return nil
	}
	if versionLess(major, minor, patch, hashSumFileMinMajor, hashSumFileMinMinor, hashSumFileMinPatch) {
		return fmt.Errorf("%w (running %s)", ErrHashSumFileUnsupported, v.Version)
	}
	return nil
}

// versionLess reports whether version a is older than version b.
func versionLess(aMajor, aMinor, aPatch, bMajor, bMinor, bPatch int) bool {
	if aMajor != bMajor {
		return aMajor < bMajor
	}
	if aMinor != bMinor {
		return aMinor < bMinor
	}
	return aPatch < bPatch
}

// versionNumbers extracts major.minor.patch from a core/version reply, using
// Decomposed when rclone supplies it and falling back to the version string.
func versionNumbers(v *Version) (major, minor, patch int, ok bool) {
	if v == nil {
		return 0, 0, 0, false
	}
	if len(v.Decomposed) >= 3 {
		return v.Decomposed[0], v.Decomposed[1], v.Decomposed[2], true
	}
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v.Version), "v"), ".", 3)
	if len(parts) < 3 {
		return 0, 0, 0, false
	}
	major, okMajor := leadingInt(parts[0])
	minor, okMinor := leadingInt(parts[1])
	patch, okPatch := leadingInt(parts[2])
	if !okMajor || !okMinor || !okPatch {
		return 0, 0, 0, false
	}
	return major, minor, patch, true
}

// leadingInt parses the leading decimal digits of s, ignoring any version
// suffix such as "-beta.1234".
func leadingInt(s string) (int, bool) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// PublicLink asks the backend for a share link to one object.
func (c *Client) PublicLink(ctx context.Context, fs, remote string) (string, error) {
	if strings.TrimSpace(fs) == "" || strings.TrimSpace(remote) == "" {
		return "", errors.New("rc: PublicLink: fs and remote are required")
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := c.Call(ctx, "operations/publiclink", map[string]string{"fs": fs, "remote": remote}, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// Cleanup empties the trash of the remote (or of one directory).
func (c *Client) Cleanup(ctx context.Context, fs, dir string) error {
	req := map[string]string{"fs": fs}
	if dir != "" {
		req["dir"] = dir
	}
	return c.Call(ctx, "operations/cleanup", req, nil)
}

// CheckResult mirrors the operations/check reply.
type CheckResult struct {
	Differ       []string `json:"differ"`
	MissingOnSrc []string `json:"missingOnSrc"`
	MissingOnDst []string `json:"missingOnDst"`
	Error        []string `json:"error"`
	HashType     string   `json:"hashType"`
	Status       string   `json:"status"`
	Success      bool     `json:"success"`
}

// Check compares two locations synchronously and returns the differences.
func (c *Client) Check(ctx context.Context, srcFs, dstFs string) (*CheckResult, error) {
	if strings.TrimSpace(srcFs) == "" || strings.TrimSpace(dstFs) == "" {
		return nil, errors.New("rc: Check: srcFs and dstFs are required")
	}
	var out CheckResult
	if err := c.Call(ctx, "operations/check",
		map[string]string{"srcFs": srcFs, "dstFs": dstFs}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Mount mounts one remote at a Windows drive letter (requires WinFsp).
func (c *Client) Mount(ctx context.Context, fs, mountPoint string) error {
	if strings.TrimSpace(fs) == "" || strings.TrimSpace(mountPoint) == "" {
		return errors.New("rc: Mount: fs and mountPoint are required")
	}
	req := map[string]any{
		"fs":         fs,
		"mountPoint": mountPoint,
		"mountOpt":   map[string]any{"ReadOnly": false},
		"vfsOpt":     map[string]any{"CacheMode": 3},
	}
	return c.Call(ctx, "mount/mount", req, nil)
}

// Unmount removes one active mount.
func (c *Client) Unmount(ctx context.Context, mountPoint string) error {
	if strings.TrimSpace(mountPoint) == "" {
		return errors.New("rc: Unmount: mountPoint is required")
	}
	return c.Call(ctx, "mount/unmount", map[string]string{"mountPoint": mountPoint}, nil)
}

// MountInfo is one entry of mount/listmounts.
type MountInfo struct {
	Fs         string `json:"Fs"`
	MountPoint string `json:"MountPoint"`
	MountedOn  string `json:"MountedOn"`
}

// ListMounts returns the active mounts.
func (c *Client) ListMounts(ctx context.Context) ([]MountInfo, error) {
	var out struct {
		MountPoints []MountInfo `json:"mountPoints"`
	}
	if err := c.Call(ctx, "mount/listmounts", struct{}{}, &out); err != nil {
		return nil, err
	}
	return out.MountPoints, nil
}
