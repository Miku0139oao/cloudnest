// Package rc is a typed client for rclone's Remote Control (RC) HTTP API.
//
// The client only uses POST, JSON bodies and HTTP Basic authentication, which
// matches how Cloudnest starts rclone rcd. It never logs request or response
// bodies, so credentials and OAuth tokens cannot leak through the rc package.
package rc

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout is the per-call timeout used by Call unless WithTimeout is
// supplied.
const DefaultTimeout = 30 * time.Second

// Client talks to one rclone rcd instance.
type Client struct {
	baseURL    string
	user       string
	pass       string
	httpClient *http.Client
	log        *slog.Logger

	// version caches the core/version reply so capability checks probe rclone
	// at most once. A failed probe is not cached. Guarded by versionMu.
	versionMu sync.Mutex
	version   *Version
}

// New creates a client for base (for example "http://127.0.0.1:5572").
// user and pass are sent with HTTP Basic authentication. log may be nil, in
// which case the client is silent.
func New(base, user, pass string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{
		baseURL: normalizeBase(base),
		user:    user,
		pass:    pass,
		httpClient: &http.Client{
			// The per-call context deadline covers the entire request. Do not
			// set Client.Timeout here: it would cap WithTimeout at the
			// default and turn deadline expiry into a retryable net.Error.
			Transport: &http.Transport{
				// rclone rcd is always on loopback. Never route RC traffic
				// through a user-configured proxy.
				Proxy:               nil,
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		log: log,
	}
}

// CloseIdleConnections closes idle keep-alive connections held by the client.
// In-flight calls are unaffected. It is safe to call more than once.
func (c *Client) CloseIdleConnections() {
	if c == nil || c.httpClient == nil {
		return
	}
	c.httpClient.CloseIdleConnections()
}

// normalizeBase trims a trailing slash and adds the http scheme when the
// caller supplied a bare host:port.
func normalizeBase(base string) string {
	base = strings.TrimSpace(base)
	base = strings.TrimRight(base, "/")
	if base != "" && !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return base
}

func (c *Client) methodURL(method string) string {
	return c.baseURL + "/" + method
}
