package rc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
// even when listing the root of fs; pass an empty string for the root.
func (c *Client) List(ctx context.Context, fs, remote string, opt ListOpt) ([]Entry, error) {
	req := listRequest{Fs: fs, Remote: remote, Opt: opt}
	var resp struct {
		List []Entry `json:"list"`
	}
	if err := c.Call(ctx, "operations/list", req, &resp); err != nil {
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
	req := syncRequest{
		SrcFs:              in.SrcFs,
		DstFs:              in.DstFs,
		CreateEmptySrcDirs: in.CreateEmptySrcDirs,
		asyncControl:       newAsyncControl(true, group, cfg),
	}
	if len(in.Exclude) > 0 {
		req.Filter = &syncFilter{Exclude: in.Exclude}
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
type syncFilter struct {
	Exclude []string `json:"Exclude,omitempty"`
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
