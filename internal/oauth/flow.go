// Package oauth runs the provider sign-in flow through `rclone authorize`.
//
// `rclone authorize` works on every supported rclone version (including 1.70,
// which has no config/oauthstatus endpoint): it prints the authorization URL
// on stderr, opens a loopback callback server on 127.0.0.1:53682, and prints
// the resulting token JSON on stdout. Cloudnest opens that URL in the system
// browser and then writes the token into the config with config/create.
package oauth

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"cloudnest/internal/platform"
)

type State string

const (
	Preparing State = "preparing"
	Awaiting  State = "awaiting_url"
	Waiting   State = "waiting"
	Done      State = "done"
	Cancelled State = "cancelled"
	Failed    State = "failed"
)

type Progress struct {
	State   State  `json:"state"`
	URL     string `json:"url,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// CreateRemoteFunc persists the token as a new remote. params carries the
// credentials the sign-in used (client_id/client_secret) which the remote must
// keep, or it would later refresh a token minted for a different OAuth client.
type CreateRemoteFunc func(ctx context.Context, provider, name, token string, params map[string]string) error

type session struct {
	id       string
	provider string
	name     string
	state    State
	url      string
	message  string
	err      string
	// params are the OAuth app credentials passed to rclone authorize.
	params map[string]string

	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
}

type Manager struct {
	rclonePath string
	create     CreateRemoteFunc
	openURL    func(string) error
	// logf receives plain state transitions so a failed sign-in can be
	// diagnosed from cloudnest.log. It may be nil.
	logf func(string, ...any)
	// logMu guards logf separately: Begin holds mu for the whole setup and
	// logs while holding it, so sharing mu here deadlocked every sign-in.
	logMu sync.Mutex

	mu       sync.Mutex
	sessions map[string]*session
	active   string
}

var ErrBusy = errors.New("oauth: another sign-in is already running")

// ErrPortBusy is returned when rclone's fixed callback port is already taken,
// usually by a sign-in process left over from an earlier attempt.
var ErrPortBusy = errors.New("oauth: the sign-in port 53682 is already in use")

var urlRE = regexp.MustCompile(`https?://[^\s"]+`)

func New(rclonePath string, create CreateRemoteFunc) *Manager {
	return &Manager{
		rclonePath: rclonePath,
		create:     create,
		openURL:    platform.OpenURL,
		sessions:   make(map[string]*session),
	}
}

// SetLogf installs a logger for sign-in diagnostics. Call it before Begin.
func (m *Manager) SetLogf(fn func(string, ...any)) {
	m.logMu.Lock()
	m.logf = fn
	m.logMu.Unlock()
}

func (m *Manager) log(format string, args ...any) {
	m.logMu.Lock()
	fn := m.logf
	m.logMu.Unlock()
	if fn != nil {
		fn(format, args...)
	}
}

// authorizeClientArgs returns rclone's positional client_id/client_secret pair.
// Both must be present or neither; a value which could be read as a flag or
// split into extra arguments is rejected rather than passed to exec.
func authorizeClientArgs(params map[string]string) (string, string, error) {
	id := strings.TrimSpace(params["client_id"])
	secret := strings.TrimSpace(params["client_secret"])
	if id == "" && secret == "" {
		return "", "", nil
	}
	if id == "" || secret == "" {
		return "", "", errors.New("oauth: a custom client needs both a client_id and a client_secret")
	}
	for _, v := range []string{id, secret} {
		if strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t\r\n") || len(v) > 512 {
			return "", "", errors.New("oauth: the client_id or client_secret is not a valid value")
		}
	}
	return id, secret, nil
}

func copyParams(params map[string]string) map[string]string {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

// Begin starts the flow. Only one flow may run at a time because rclone binds
// a single fixed callback port (127.0.0.1:53682). params, when it carries
// client_id and client_secret, makes rclone authorize with the user's own OAuth
// app instead of rclone's shared one.
func (m *Manager) Begin(provider, name string, params map[string]string) (string, error) {
	clientArg, secretArg, err := authorizeClientArgs(params)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != "" {
		if s := m.sessions[m.active]; s != nil {
			if s.state == Preparing || s.state == Awaiting || s.state == Waiting {
				return "", ErrBusy
			}
		}
	}
	id := randomID()
	// rclone binds one fixed port for the browser callback. A leftover process
	// from an earlier attempt would make this sign-in hang forever.
	if err := probeCallbackPort(); err != nil {
		return "", ErrPortBusy
	}
	m.log("oauth: begin provider=%s name=%s custom_client=%v", provider, name, clientArg != "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	s := &session{id: id, provider: provider, name: name, state: Preparing, ctx: ctx, cancel: cancel, params: copyParams(params)}
	m.sessions[id] = s
	m.active = id

	args := []string{"authorize", provider}
	if clientArg != "" {
		// rclone takes the pair as positional arguments. They are visible in the
		// process list while the sign-in runs, which matches where the config
		// already keeps them; nothing logs them.
		args = append(args, clientArg, secretArg)
	}
	args = append(args, "--auth-no-open-browser")
	cmd := exec.CommandContext(ctx, m.rclonePath, args...)
	addHiddenWindow(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		delete(m.sessions, id)
		m.active = ""
		cancel()
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		delete(m.sessions, id)
		m.active = ""
		cancel()
		return "", err
	}
	if err := cmd.Start(); err != nil {
		delete(m.sessions, id)
		m.active = ""
		cancel()
		return "", err
	}
	s.cmd = cmd

	go m.run(s, stdout, stderr)
	return id, nil
}

func (m *Manager) run(s *session, stdout io.ReadCloser, stderr io.ReadCloser) {
	var outBuf strings.Builder
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		_, _ = io.Copy(&outBuf, stdout)
	}()

	sc := bufio.NewScanner(stderr)
	var tail []string
	for sc.Scan() {
		line := sc.Text()
		tail = append(tail, line)
		if len(tail) > 40 {
			tail = tail[1:]
		}
		if s.url == "" {
			if u := urlRE.FindString(line); strings.Contains(u, "127.0.0.1:53682/auth") {
				m.mu.Lock()
				s.url = u
				s.state = Waiting
				m.mu.Unlock()
				m.log("oauth: authorization url ready")
				_ = m.openURL(u)
			}
		}
	}

	// Both pipes must be drained before Wait: Wait closes them, and a token
	// still sitting in the stdout pipe would otherwise be lost.
	<-outDone
	waitErr := s.cmd.Wait()

	m.mu.Lock()
	if s.state == Cancelled {
		m.mu.Unlock()
		m.finish(s)
		return
	}
	m.mu.Unlock()

	// Some providers print the token and then exit non-zero; a token is what
	// actually matters, so it is checked first.
	token := extractToken(outBuf.String())
	if token == "" {
		msg := "sign-in finished but rclone did not return a token"
		if waitErr != nil {
			msg = joinTail(tail, waitErr)
		}
		m.mu.Lock()
		s.state = Failed
		s.err = msg
		m.mu.Unlock()
		m.log("oauth: failed: %s", msg)
		m.finish(s)
		return
	}
	if waitErr != nil {
		m.log("oauth: rclone exited with %v after returning a token", waitErr)
	}

	// Persist the token. This is a short synchronous call.
	if err := m.create(s.ctx, s.provider, s.name, token, s.params); err != nil {
		m.mu.Lock()
		s.state = Failed
		s.err = err.Error()
		m.mu.Unlock()
		m.log("oauth: saving the token failed: %v", err)
		m.finish(s)
		return
	}
	m.mu.Lock()
	s.state = Done
	s.message = "connected"
	m.mu.Unlock()
	m.log("oauth: connected provider=%s name=%s", s.provider, s.name)
	m.finish(s)
}

func (m *Manager) finish(s *session) {
	m.mu.Lock()
	if m.active == s.id {
		m.active = ""
	}
	m.mu.Unlock()
	s.cancel()
}

func (m *Manager) Poll(id string) Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return Progress{State: Failed, Error: "sign-in session not found"}
	}
	return Progress{State: s.state, URL: s.url, Message: s.message, Error: s.err}
}

// Retry restarts the request behind a finished session with the same provider,
// remote name and OAuth client. A retry must not fall back to the defaults:
// the name may be an existing remote, and the client may be the user's own.
func (m *Manager) Retry(id string) (string, error) {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil {
		m.mu.Unlock()
		return "", errors.New("oauth: sign-in session not found")
	}
	provider, name := s.provider, s.name
	params := copyParams(s.params)
	m.mu.Unlock()
	return m.Begin(provider, name, params)
}

func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s != nil {
		s.state = Cancelled
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		s.cancel()
	}
	m.mu.Unlock()
}

// ---------------------------------------------------------------- helpers

func extractToken(out string) string {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return ""
	}
	raw := out[start : end+1]
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return ""
	}
	if _, ok := probe["access_token"]; !ok {
		if _, ok := probe["refresh_token"]; !ok {
			return ""
		}
	}
	return raw
}

func joinTail(lines []string, err error) string {
	start := 0
	if len(lines) > 6 {
		start = len(lines) - 6
	}
	msg := strings.Join(lines[start:], " ")
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = err.Error()
	}
	return msg
}

// probeCallbackPort reports whether rclone can still bind its fixed OAuth
// callback port. A leftover authorize process would otherwise leave the new
// sign-in waiting forever with no visible error.
func probeCallbackPort() error {
	ln, err := net.Listen("tcp", "127.0.0.1:53682")
	if err != nil {
		return err
	}
	return ln.Close()
}

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
