package rc

import (
	"encoding/json"
	"time"
)

// Version is the response from core/version.
type Version struct {
	Version    string `json:"version"`
	Decomposed []int  `json:"decomposed,omitempty"`
	IsGit      bool   `json:"isGit,omitempty"`
	IsBeta     bool   `json:"isBeta,omitempty"`
	OS         string `json:"os,omitempty"`
	Arch       string `json:"arch,omitempty"`
	GoVersion  string `json:"goVersion,omitempty"`
	Linking    string `json:"linking,omitempty"`
	GoTags     string `json:"goTags,omitempty"`
}

// Remote is a configured rclone remote. ListRemotes fills Type only when the
// server includes it (rclone 1.70's config/listremotes returns names only).
type Remote struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// UnmarshalJSON accepts both a bare remote name and an object with "name" or
// "Name" fields. This keeps ListRemotes working across rclone versions.
func (r *Remote) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		r.Name = name
		r.Type = ""
		return nil
	}
	var raw struct {
		Name      string `json:"name"`
		NameUpper string `json:"Name"`
		Type      string `json:"type"`
		TypeUpper string `json:"Type"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Name = raw.Name
	if r.Name == "" {
		r.Name = raw.NameUpper
	}
	r.Type = raw.Type
	if r.Type == "" {
		r.Type = raw.TypeUpper
	}
	return nil
}

// Provider describes an rclone storage backend returned by config/providers.
type Provider struct {
	Name        string           `json:"Name"`
	Description string           `json:"Description"`
	Prefix      string           `json:"Prefix"`
	Options     []ProviderOption `json:"Options"`
}

// ProviderOption is one configuration option for a storage backend. The field
// names mirror rclone's options/info JSON (capitalised).
type ProviderOption struct {
	Name       string          `json:"Name"`
	Help       string          `json:"Help"`
	Type       string          `json:"Type"`
	Default    any             `json:"Default"`
	Required   bool            `json:"Required"`
	IsPassword bool            `json:"IsPassword"`
	Advanced   bool            `json:"Advanced"`
	Hide       int             `json:"Hide"`
	Provider   string          `json:"Provider,omitempty"`
	Sensitive  bool            `json:"Sensitive"`
	Examples   []OptionExample `json:"Examples,omitempty"`
	Value      any             `json:"Value,omitempty"`
	NoPrefix   bool            `json:"NoPrefix,omitempty"`
	Exclusive  bool            `json:"Exclusive,omitempty"`
	DefaultStr string          `json:"DefaultStr,omitempty"`
	ValueStr   string          `json:"ValueStr,omitempty"`
	FieldName  string          `json:"FieldName,omitempty"`
}

// OptionExample is a sample value shown for a provider option.
type OptionExample struct {
	Value string `json:"Value"`
	Help  string `json:"Help"`
}

// Entry is one item returned by operations/list or operations/stat. The field
// names match rclone's lsjson output.
type Entry struct {
	Path     string            `json:"Path"`
	Name     string            `json:"Name"`
	Size     int64             `json:"Size"`
	ModTime  time.Time         `json:"ModTime"`
	IsDir    bool              `json:"IsDir"`
	MimeType string            `json:"MimeType,omitempty"`
	Hashes   map[string]string `json:"Hashes,omitempty"`
}

// JobStatus is the response from job/status.
type JobStatus struct {
	ID        int64     `json:"id"`
	Finished  bool      `json:"finished"`
	Success   bool      `json:"success"`
	Error     string    `json:"error"`
	Duration  float64   `json:"duration"`
	Output    any       `json:"output"`
	Group     string    `json:"group,omitempty"`
	StartTime time.Time `json:"startTime"`
	EndTime   time.Time `json:"endTime"`
	Progress  any       `json:"progress,omitempty"`
}

// JobList is the response from job/list.
//
// rclone 1.70 returns executeId and a single jobids array. Some versions also
// report runningIds/finishedIds; both shapes are decoded here.
type JobList struct {
	ExecuteID   string  `json:"executeId"`
	JobIDs      []int64 `json:"jobids,omitempty"`
	RunningIDs  []int64 `json:"runningIds,omitempty"`
	FinishedIDs []int64 `json:"finishedIds,omitempty"`
}

// Transferring describes one active file transfer in core/stats.
type Transferring struct {
	Name       string   `json:"name"`
	Size       int64    `json:"size"`
	Bytes      int64    `json:"bytes"`
	Percentage int      `json:"percentage"`
	Speed      float64  `json:"speed"`
	SpeedAvg   float64  `json:"speedAvg,omitempty"`
	ETA        *float64 `json:"eta,omitempty"`
	Group      string   `json:"group,omitempty"`
	SrcFs      string   `json:"srcFs,omitempty"`
	DstFs      string   `json:"dstFs,omitempty"`
}

// Stats is the response from core/stats.
//
// ETA is a pointer because rclone reports null when it cannot estimate a
// remaining time. Transferred is included for callers which map it from a
// higher level; core/stats itself reports completed transfers through
// core/transferred (see TransferredItem).
type Stats struct {
	Bytes               int64          `json:"bytes"`
	Checks              int64          `json:"checks"`
	Deletes             int64          `json:"deletes"`
	DeletedDirs         int64          `json:"deletedDirs"`
	ElapsedTime         float64        `json:"elapsedTime"`
	Errors              int64          `json:"errors"`
	ETA                 *float64       `json:"eta"`
	FatalError          bool           `json:"fatalError"`
	LastError           string         `json:"lastError,omitempty"`
	Listed              int64          `json:"listed"`
	Renames             int64          `json:"renames"`
	RetryError          bool           `json:"retryError"`
	Speed               float64        `json:"speed"`
	TotalBytes          int64          `json:"totalBytes"`
	TotalChecks         int64          `json:"totalChecks"`
	TotalTransfers      int64          `json:"totalTransfers"`
	TransferTime        float64        `json:"transferTime"`
	Transfers           int64          `json:"transfers"`
	Transferred         int64          `json:"transferred,omitempty"`
	Transferring        []Transferring `json:"transferring,omitempty"`
	Checking            []string       `json:"checking,omitempty"`
	ServerSideCopies    int64          `json:"serverSideCopies"`
	ServerSideCopyBytes int64          `json:"serverSideCopyBytes"`
	ServerSideMoves     int64          `json:"serverSideMoves"`
	ServerSideMoveBytes int64          `json:"serverSideMoveBytes"`
}

// TransferredItem is one completed transfer from core/transferred.
//
// rclone 1.70.3 reports started_at/completed_at as RFC3339 strings and no
// timestamp field. Timestamp is kept as any because newer builds have used
// both numeric and string forms; decoding must not fail on it.
type TransferredItem struct {
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	Bytes       int64     `json:"bytes"`
	Checked     bool      `json:"checked"`
	Timestamp   any       `json:"timestamp,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Error       string    `json:"error"`
	JobID       int64     `json:"jobid,omitempty"`
	Group       string    `json:"group,omitempty"`
	SrcFs       string    `json:"srcFs,omitempty"`
	DstFs       string    `json:"dstFs,omitempty"`
}

// OAuthStatus is the decoded response from config/oauthstatus. rclone builds
// newer than the currently bundled 1.70.3 expose this method; older builds
// reply with a not-found Error.
type OAuthStatus struct {
	Status  string `json:"status,omitempty"`
	State   string `json:"state,omitempty"`
	AuthURL string `json:"authUrl,omitempty"`
	Error   string `json:"error,omitempty"`
	Name    string `json:"name,omitempty"`
}

// Usage is the response from operations/about.
type Usage struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
	Free  int64 `json:"free"`
}

// CopyFileIn is the request body for operations/copyfile.
//
// Group is not part of the wire format: it only lets an asynchronous copy
// join the backup's job group so one pause covers the whole transfer.
type CopyFileIn struct {
	SrcFs     string `json:"srcFs"`
	SrcRemote string `json:"srcRemote"`
	DstFs     string `json:"dstFs"`
	DstRemote string `json:"dstRemote"`

	Group string `json:"-"`
}

// ListOpt controls operations/list and operations/stat.
type ListOpt struct {
	Recurse       bool     `json:"recurse,omitempty"`
	NoModTime     bool     `json:"noModTime,omitempty"`
	ShowEncrypted bool     `json:"showEncrypted,omitempty"`
	ShowOrigIDs   bool     `json:"showOrigIDs,omitempty"`
	ShowHash      bool     `json:"showHash,omitempty"`
	NoMimeType    bool     `json:"noMimeType,omitempty"`
	DirsOnly      bool     `json:"dirsOnly,omitempty"`
	FilesOnly     bool     `json:"filesOnly,omitempty"`
	Metadata      bool     `json:"metadata,omitempty"`
	HashTypes     []string `json:"hashTypes,omitempty"`
}

// SyncIn is the caller-facing description of a copy or sync job.
//
// The RC control fields (_async, _group and _config) are deliberately not
// part of SyncIn's JSON; StartSync adds them through the internal
// asyncControl helper. Group, Transfers, Checkers and DryRun drive those
// control fields.
type SyncIn struct {
	SrcFs              string `json:"srcFs"`
	DstFs              string `json:"dstFs"`
	CreateEmptySrcDirs bool   `json:"createEmptySrcDirs,omitempty"`

	Group     string   `json:"-"`
	Transfers int      `json:"-"`
	Checkers  int      `json:"-"`
	DryRun    bool     `json:"-"`
	BwLimit   string   `json:"-"`
	Exclude   []string `json:"-"`
	// Retries maps to rclone's Retries option. 1 means "do not retry", which
	// is how the Settings auto-retry switch is applied.
	Retries int `json:"-"`
}

// ConfigOpt is the opt dictionary accepted by config/create and
// config/update.
type ConfigOpt struct {
	Obscure        bool   `json:"obscure,omitempty"`
	NoObscure      bool   `json:"noObscure,omitempty"`
	NoOutput       bool   `json:"noOutput,omitempty"`
	NonInteractive bool   `json:"nonInteractive,omitempty"`
	Continue       bool   `json:"continue,omitempty"`
	All            bool   `json:"all,omitempty"`
	State          string `json:"state,omitempty"`
	Result         string `json:"result,omitempty"`
}

// CreateRemoteIn is the input for config/create.
//
// ConfigIsLocal and ConfigAuthNoBrowser are string values ("true") because
// that is how rclone's RC layer expects the corresponding configuration
// parameters. When Async is set, CreateRemote adds _async/_group to the
// request and the response carries a JobID.
type CreateRemoteIn struct {
	Name                string         `json:"name"`
	Type                string         `json:"type"`
	Parameters          map[string]any `json:"parameters,omitempty"`
	Opt                 *ConfigOpt     `json:"opt,omitempty"`
	ConfigIsLocal       string         `json:"config_is_local,omitempty"`
	ConfigAuthNoBrowser string         `json:"config_auth_no_browser,omitempty"`

	Async bool   `json:"-"`
	Group string `json:"-"`
}

// ConfigResult is the response from config/create or config/update. The
// PascalCase fields are what rclone returns for a synchronous call; JobID is
// populated when the call was made asynchronously.
type ConfigResult struct {
	Error  string `json:"Error"`
	Option any    `json:"Option"`
	Result string `json:"Result"`
	State  string `json:"State"`
	JobID  int64  `json:"jobid,omitempty"`
}
