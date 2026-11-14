// Package dto holds the wire types shared with the Svelte frontend.
package dto

// Fail is a stable, UI-friendly error. Bound methods return data with Err set
// instead of a Go error whenever the failure is something the user can act on.
type Fail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Retry   bool   `json:"retry"`
}

type Empty struct{}

// ---------------------------------------------------------------- bootstrap

type EngineStatus struct {
	State      string `json:"state"`
	Version    string `json:"version"`
	ConfigPath string `json:"configPath,omitempty"`
	Port       int    `json:"port,omitempty"`
	Err        *Fail  `json:"err,omitempty"`
}

type Bootstrap struct {
	Version    string       `json:"version"`
	Language   string       `json:"language"`
	Theme      string       `json:"theme"`
	Onboarded  bool         `json:"onboarded"`
	Engine     EngineStatus `json:"engine"`
	Accounts   []Account    `json:"accounts"`
	Recent     []Record     `json:"recent"`
	Err        *Fail        `json:"err,omitempty"`
	DataDir    string       `json:"dataDir"`
	PortableOK bool         `json:"portableOk"`
}

// ---------------------------------------------------------------- accounts

type Account struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Name       string `json:"name"`
	Email      string `json:"email,omitempty"`
	Status     string `json:"status"` // connected | relogin
	Used       int64  `json:"used"`
	Total      int64  `json:"total"`
	LastBackup int64  `json:"lastBackup,omitempty"`
}

type AccountList struct {
	Items []Account `json:"items"`
	Err   *Fail     `json:"err,omitempty"`
}

type Usage struct {
	Used  int64 `json:"used"`
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
	Err   *Fail `json:"err,omitempty"`
}

type ProviderExample struct {
	Value string `json:"value"`
	Help  string `json:"help"`
}

// ProviderOption mirrors rclone's fs.Option (the fields a form needs).
type ProviderOption struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Default   string            `json:"default"`
	Help      string            `json:"help"`
	Required  bool              `json:"required"`
	Password  bool              `json:"password"`
	Advanced  bool              `json:"advanced"`
	Sensitive bool              `json:"sensitive"`
	Provider  string            `json:"provider,omitempty"`
	Examples  []ProviderExample `json:"examples,omitempty"`
}

type Provider struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Prefix      string           `json:"prefix"`
	Options     []ProviderOption `json:"options"`
}

type ProviderCatalog struct {
	Items []Provider `json:"items"`
	Err   *Fail      `json:"err,omitempty"`
}

type ConnectSession struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	DisplayName string `json:"displayName"`
	Err         *Fail  `json:"err,omitempty"`
}

type ConnectProgress struct {
	State   string `json:"state"` // preparing | awaiting_url | waiting | done | failed | cancelled
	AuthURL string `json:"authUrl,omitempty"`
	Message string `json:"message,omitempty"`
	Err     *Fail  `json:"err,omitempty"`
}

// ---------------------------------------------------------------- files

type SplitRef struct {
	State        string `json:"state"` // ready | broken | incomplete | foreign
	Parts        int    `json:"parts"`
	OriginalName string `json:"originalName"`
	Size         int64  `json:"size"`
}

type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Mime    string    `json:"mime,omitempty"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime int64     `json:"modTime"`
	Split   *SplitRef `json:"split,omitempty"`
}

type Listing struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
	Err       *Fail   `json:"err,omitempty"`
}

type ListOptions struct {
	ShowHidden bool `json:"showHidden"`
}

type Measure struct {
	Files     int   `json:"files"`
	Bytes     int64 `json:"bytes"`
	Estimated bool  `json:"estimated"`
	Err       *Fail `json:"err,omitempty"`
}

type Root struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

type RootList struct {
	Items []Root `json:"items"`
	Err   *Fail  `json:"err,omitempty"`
}

// ---------------------------------------------------------------- transfers

type JobRef struct {
	ID    string `json:"id"`
	JobID int64  `json:"jobId"`
	Group string `json:"group"`
	Err   *Fail  `json:"err,omitempty"`
}

type JobProgress struct {
	ID          string  `json:"id"`
	JobID       int64   `json:"jobId"`
	Kind        string  `json:"kind"`  // backup | restore | upload | download
	State       string  `json:"state"` // running | paused | done | failed | cancelled
	Title       string  `json:"title,omitempty"`
	Current     string  `json:"current,omitempty"`
	DisplayName string  `json:"displayName,omitempty"`
	Phase       string  `json:"phase,omitempty"`
	Bytes       int64   `json:"bytes"`
	TotalBytes  int64   `json:"totalBytes"`
	Speed       float64 `json:"speed"`
	ETA         *int64  `json:"eta,omitempty"`
	Done        int     `json:"done"`
	TotalFiles  int     `json:"totalFiles"`
	Failed      int     `json:"failedFiles"`
	Split       bool    `json:"split"`
	// Error is rclone's message when a job finished unsuccessfully.
	Error      string `json:"error,omitempty"`
	PartsDone  int    `json:"partsDone,omitempty"`
	PartsTotal int    `json:"partsTotal,omitempty"`
}

type Snapshot struct {
	Seq        uint64        `json:"seq"`
	At         int64         `json:"at"`
	Idle       bool          `json:"idle"`
	Jobs       []JobProgress `json:"jobs"`
	Bytes      int64         `json:"bytes"`
	TotalBytes int64         `json:"totalBytes"`
	Speed      float64       `json:"speed"`
	ETA        *int64        `json:"eta,omitempty"`
	Series     []float64     `json:"series"`
}

type Record struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	Title      string `json:"title"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
	Bytes      int64  `json:"bytes"`
	Files      int    `json:"files"`
	Failed     int    `json:"failed"`
	Error      string `json:"error,omitempty"`
}

type RecordList struct {
	Items []Record `json:"items"`
	Err   *Fail    `json:"err,omitempty"`
}

type JobProgressList struct {
	Items []JobProgress `json:"items"`
	Err   *Fail         `json:"err,omitempty"`
}

type DeletedItem struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type PlanRequest struct {
	SourcePaths []string `json:"sourcePaths"`
	AccountID   string   `json:"accountId"`
	DestRoot    string   `json:"destRoot"`
	Mode        string   `json:"mode"` // additive | mirror
	FastFiles   bool     `json:"fastFiles"`
	Transfers   int      `json:"transfers"`
	Excludes    []string `json:"excludes"`
}

type BackupPlan struct {
	Files       int           `json:"files"`
	Bytes       int64         `json:"bytes"`
	Estimated   bool          `json:"estimated"`
	Warnings    []string      `json:"warnings,omitempty"`
	DeleteCount int           `json:"deleteCount"`
	DeleteBytes int64         `json:"deleteBytes"`
	Deletions   []DeletedItem `json:"deletions,omitempty"`
	Token       string        `json:"token"`
	Err         *Fail         `json:"err,omitempty"`
}

type PlanResult struct {
	Plan BackupPlan `json:"plan"`
	Err  *Fail      `json:"err,omitempty"`
}

type JobDetail struct {
	Record Record        `json:"record"`
	Jobs   []JobProgress `json:"jobs,omitempty"`
	Err    *Fail         `json:"err,omitempty"`
}

// ---------------------------------------------------------------- misc

type PathPick struct {
	Path      string   `json:"path,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Cancelled bool     `json:"cancelled"`
	Err       *Fail    `json:"err,omitempty"`
}

type Settings struct {
	Language  string `json:"language"`
	Theme     string `json:"theme"`
	Onboarded bool   `json:"onboarded"`
	Backup    struct {
		Transfers  int    `json:"transfers"`
		BwLimit    string `json:"bwLimit"`
		AutoRetry  bool   `json:"autoRetry"`
		FastFiles  bool   `json:"fastFiles"`
		PartSizeMB int    `json:"partSizeMB"`
	} `json:"backup"`
	Advanced struct {
		Proxy      bool   `json:"proxy"`
		Verbose    bool   `json:"verbose"`
		EnginePath string `json:"enginePath"`
	} `json:"advanced"`
	Window struct {
		Width  int `json:"width"`
		Height int `json:"height"`
		X      int `json:"x"`
		Y      int `json:"y"`
	} `json:"window"`
}

// ---------------------------------------------------------------- file ops

// DeleteResult reports the outcome of DeleteCloud. Failed holds the paths that
// could not be removed; Err carries the first failure for UI display.
type DeleteResult struct {
	Deleted int      `json:"deleted"`
	Failed  []string `json:"failed,omitempty"`
	Err     *Fail    `json:"err,omitempty"`
}

// SplitInfo describes a remote .cloudbox-part directory. PartCount is the
// manifest's expected part count and Parts is how many parts were found.
type SplitInfo struct {
	State        string `json:"state"`
	Parts        int    `json:"parts"`
	PartCount    int    `json:"partCount"`
	OriginalName string `json:"originalName"`
	Size         int64  `json:"size"`
	Missing      []int  `json:"missing,omitempty"`
	Err          *Fail  `json:"err,omitempty"`
}
