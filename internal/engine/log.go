package engine

import (
	"regexp"
	"strings"
	"sync"
)

var (
	// Authorization headers (the credential part only).
	reAuthHeader = regexp.MustCompile(`(?i)(Authorization:\s*(?:Basic|Bearer)\s+)[^\s"']+`)

	// OAuth URLs are removed wholesale. They frequently contain code, state
	// and token query parameters and must never reach a log file.
	reOAuthURL = regexp.MustCompile(`(?i)https?://[^\s"'<>]*(?:oauth2?|accounts\.google\.com|login\.microsoftonline\.com|dropbox\.com/oauth|[?&](?:code|state|access_token|refresh_token)=)[^\s"'<>]*`)

	// JSON-style secrets: "access_token":"...", "authUrl":"...", etc.
	reJSONSecret = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|client_secret|id_token|token|password|authUrl|auth_url|state)"\s*:\s*")[^"]*(")`)

	// key=value (or key: value) style secrets, including query strings. The
	// whitespace tolerance catches rclone's "option = value" output and
	// RCLONE_RC_PASS=... environment dumps.
	reKeyValueSecret = regexp.MustCompile(`(?i)\b(access_token|refresh_token|client_secret|id_token|token|password|pass|rclone_rc_pass|rc[-_]pass|state)\s*[=:]\s*([^&\s"'<>]+)`)
)

// Redact removes credentials, OAuth URLs and token values from a log line.
// The function is intentionally conservative: it may redact more than
// strictly necessary, but it never returns a line containing a known secret.
func Redact(line string) string {
	line = reAuthHeader.ReplaceAllString(line, `${1}[REDACTED]`)
	line = reOAuthURL.ReplaceAllString(line, "[REDACTED-URL]")
	line = reJSONSecret.ReplaceAllString(line, `${1}[REDACTED]${2}`)
	line = reKeyValueSecret.ReplaceAllString(line, `${1}=[REDACTED]`)
	return line
}

// RingBuffer keeps the last N lines written to it. It is safe for concurrent
// use and is used to retain redacted rclone output for diagnostics.
type RingBuffer struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial string
}

// NewRingBuffer creates a ring buffer holding at most max lines. A
// non-positive max uses 200.
func NewRingBuffer(max int) *RingBuffer {
	if max <= 0 {
		max = 200
	}
	return &RingBuffer{max: max}
}

// Append adds one complete line.
func (r *RingBuffer) Append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.push(line)
}

// Write implements io.Writer. Incomplete trailing lines are buffered until a
// newline arrives; call Flush to emit a final partial line.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.partial + string(p)
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		r.push(strings.TrimSuffix(s[:i], "\r"))
		s = s[i+1:]
	}
	r.partial = s
	return len(p), nil
}

// Flush appends any buffered partial line.
func (r *RingBuffer) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.partial != "" {
		r.push(r.partial)
		r.partial = ""
	}
}

func (r *RingBuffer) push(line string) {
	if r.max <= 0 {
		return
	}
	if len(r.lines) < r.max {
		r.lines = append(r.lines, line)
		return
	}
	copy(r.lines, r.lines[1:])
	r.lines[len(r.lines)-1] = line
}

// Lines returns a copy of the buffered lines, oldest first.
func (r *RingBuffer) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

// String joins the buffered lines with newlines.
func (r *RingBuffer) String() string {
	return strings.Join(r.Lines(), "\n")
}

// Len reports how many complete lines are buffered.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lines)
}

// lineWriter turns arbitrary process output chunks into redacted lines and
// forwards them to emit. It is used for rclone's stdout and stderr.
type lineWriter struct {
	mu      sync.Mutex
	partial string
	emit    func(string)
}

const maxPartialLine = 64 * 1024

func newLineWriter(emit func(string)) *lineWriter {
	return &lineWriter{emit: emit}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	if w == nil || w.emit == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	s := w.partial + string(p)
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		w.emit(strings.TrimSuffix(s[:i], "\r"))
		s = s[i+1:]
	}
	if len(s) > maxPartialLine {
		w.emit(s[:maxPartialLine] + "...")
		s = ""
	}
	w.partial = s
	return len(p), nil
}

// Flush emits a trailing partial line, if any.
func (w *lineWriter) Flush() {
	if w == nil || w.emit == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.partial != "" {
		w.emit(w.partial)
		w.partial = ""
	}
}
