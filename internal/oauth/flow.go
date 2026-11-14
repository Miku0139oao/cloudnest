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

// CreateRemoteFunc persists the token as a new remote.
type CreateRemoteFunc func(ctx context.Context, provider, name, token string) error

type session struct {
	id       string
	provider string
	name     string
	state    State
	url      string
	message  string
	err      string

	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
}

type Manager struct {
	rclonePath string
	create     CreateRemoteFunc
	openURL    func(string) error

	mu       sync.Mutex
	sessions map[string]*session
	active   string
}

var ErrBusy = errors.New("oauth: another sign-in is already running")

var urlRE = regexp.MustCompile(`https?://[^\s"]+`)

func New(rclonePath string, create CreateRemoteFunc) *Manager {
	return &Manager{
		rclonePath: rclonePath,
		create:     create,
		openURL:    platform.OpenURL,
		sessions:   make(map[string]*session),
	}
}

// Begin starts the flow. Only one flow may run at a time because rclone binds
// a single fixed callback port (127.0.0.1:53682).
func (m *Manager) Begin(provider, name string) (string, error) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	s := &session{id: id, provider: provider, name: name, state: Preparing, ctx: ctx, cancel: cancel}
	m.sessions[id] = s
	m.active = id

	cmd := exec.CommandContext(ctx, m.rclonePath, "authorize", provider, "--auth-no-open-browser")
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
				_ = m.openURL(u)
			}
		}
	}

	waitErr := s.cmd.Wait()
	<-outDone

	m.mu.Lock()
	if s.state == Cancelled {
		m.mu.Unlock()
		m.finish(s)
		return
	}
	if waitErr != nil {
		s.state = Failed
		s.err = joinTail(tail, waitErr)
		m.mu.Unlock()
		m.finish(s)
		return
	}
	m.mu.Unlock()

	token := extractToken(outBuf.String())
	if token == "" {
		m.mu.Lock()
		s.state = Failed
		s.err = "sign-in finished but rclone did not return a token"
		m.mu.Unlock()
		m.finish(s)
		return
	}

	// Persist the token. This is a short synchronous call.
	if err := m.create(s.ctx, s.provider, s.name, token); err != nil {
		m.mu.Lock()
		s.state = Failed
		s.err = err.Error()
		m.mu.Unlock()
		m.finish(s)
		return
	}
	m.mu.Lock()
	s.state = Done
	s.message = "connected"
	m.mu.Unlock()
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

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
