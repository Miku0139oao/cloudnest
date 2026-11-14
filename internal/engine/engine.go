// Package engine supervises the rclone rcd process used by Cloudnest.
//
// An Engine resolves an rclone binary, starts rclone's Remote Control server
// on a random loopback port with random Basic-auth credentials, keeps it alive
// (bounded restart policy) and exposes a typed RC client. The rclone process
// is hidden, assigned to a Windows Job Object configured with
// KILL_ON_JOB_CLOSE, and stopped gracefully through core/quit.
package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"cloudnest/internal/platform"
	"cloudnest/internal/rc"
)

// State is the lifecycle state reported by an Engine.
type State string

const (
	// StateStarting means rclone is being located or launched.
	StateStarting State = "starting"
	// StateReady means the RC server passed its health check.
	StateReady State = "ready"
	// StateRestarting means rclone exited unexpectedly and is being relaunched.
	StateRestarting State = "restarting"
	// StateFailed means startup or repeated restarts failed.
	StateFailed State = "failed"
	// StateStopped means the engine is not running.
	StateStopped State = "stopped"
)

// String implements fmt.Stringer.
func (s State) String() string { return string(s) }

// Status is an immutable snapshot of an Engine.
//
// Since is the time the current State was entered. Err is nil unless State is
// StateFailed (or a transient restart error is being reported while
// StateRestarting). Err text is redacted before it is stored.
type Status struct {
	State      State
	Version    string
	Port       int
	ConfigPath string
	Err        error
	Since      time.Time
}

// Engine manages the rclone rcd process.
type Engine interface {
	// Start launches rclone and waits until its RC server is healthy.
	Start(ctx context.Context) error
	// Stop asks rclone to quit, waits five seconds, then kills it.
	Stop(ctx context.Context) error
	// Restart stops and starts the engine.
	Restart(ctx context.Context) error
	// Status returns the current snapshot.
	Status() Status
	// Client returns the typed RC client. It fails unless the engine is ready.
	Client() (*rc.Client, error)
	// Subscribe registers a status callback and returns an idempotent
	// unsubscribe function. The callback is invoked from a single notifier
	// goroutine, never while engine locks are held.
	Subscribe(fn func(Status)) (unsubscribe func())
	// ConfigPath returns the rclone config file Cloudnest uses.
	ConfigPath() string
}

var (
	// ErrNotReady is returned by Client before the engine is ready.
	ErrNotReady = errors.New("engine: rclone is not ready")
	// ErrNoBinary is returned when no rclone executable can be found.
	ErrNoBinary = errors.New("engine: rclone executable not found")
	// ErrVersionTooOld is returned when rclone is older than 1.70.0.
	ErrVersionTooOld = errors.New("engine: rclone version is too old")
)

// Options configures New.
type Options struct {
	// RclonePath overrides discovery and uses this executable directly.
	RclonePath string
	// AppRoot overrides the directory used for bundled-binary discovery.
	AppRoot string
	// DataDir overrides the mutable data directory.
	DataDir string
	// ConfigPath overrides <DataDir>/rclone.conf.
	ConfigPath string
	// Logger receives engine diagnostics. nil disables logging.
	Logger *slog.Logger
	// LogWriter receives redacted lines from rclone's stdout/stderr. It may be
	// called from the process-output goroutine.
	LogWriter func(line string)
}

// New creates an Engine. Paths are resolved eagerly when possible; Start
// resolves them again and reports any failure through Status and its return
// value.
func New(opts Options) Engine {
	if opts.AppRoot == "" {
		if root, err := platform.AppRoot(); err == nil {
			opts.AppRoot = root
		}
	}
	if opts.DataDir == "" {
		if dir, err := platform.DataDir(); err == nil {
			opts.DataDir = dir
		}
	}
	if opts.ConfigPath == "" && opts.DataDir != "" {
		opts.ConfigPath = filepath.Join(opts.DataDir, "rclone.conf")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	e := &engine{
		opts:     opts,
		ring:     NewRingBuffer(200),
		notifyCh: make(chan struct{}, 1),
		subs:     make(map[uint64]func(Status)),
	}
	e.status = Status{State: StateStopped, ConfigPath: opts.ConfigPath, Since: time.Now()}
	return e
}
