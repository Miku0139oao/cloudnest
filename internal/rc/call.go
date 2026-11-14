package rc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// CallOption configures one Call.
type CallOption func(*callOptions)

type callOptions struct {
	timeout time.Duration
	retries int
}

// WithTimeout overrides the per-call timeout. A non-positive duration is
// ignored. Each retry attempt gets its own timeout.
func WithTimeout(timeout time.Duration) CallOption {
	return func(o *callOptions) {
		if timeout > 0 {
			o.timeout = timeout
		}
	}
}

// WithRetry enables up to n retries for idempotent methods when a call fails
// with a transport error or EOF. Retries never apply to config/create,
// sync/* or job/*. n is the number of retries after the first attempt, so
// WithRetry(2) means at most three HTTP requests.
func WithRetry(n int) CallOption {
	return func(o *callOptions) {
		if n < 0 {
			n = 0
		}
		o.retries = n
	}
}

// Call invokes method with in encoded as JSON and decodes the response into
// out.
//
// out may be nil when the caller is not interested in the response. If out is
// non-nil and the response body is empty, Call succeeds without modifying out.
// A non-2xx response is returned as *Error.
func (c *Client) Call(ctx context.Context, method string, in any, out any, opts ...CallOption) error {
	if c == nil {
		return errors.New("rc: nil client")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	method = strings.TrimPrefix(strings.TrimSpace(method), "/")
	if method == "" {
		return errors.New("rc: empty method")
	}

	settings := callOptions{timeout: DefaultTimeout}
	for _, opt := range opts {
		if opt != nil {
			opt(&settings)
		}
	}
	body, err := encodeInput(in)
	if err != nil {
		return err
	}

	canRetry := isIdempotent(method)
	var lastErr error
	for attempt := 0; attempt <= settings.retries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * 100 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		err = c.doOnce(ctx, method, body, out, settings.timeout)
		if err == nil {
			return nil
		}
		lastErr = err
		if !canRetry || !isRetryable(err) || attempt == settings.retries {
			break
		}
		c.log.Debug("rc: retrying idempotent call", "method", method, "attempt", attempt+1)
	}
	return lastErr
}

// doOnce performs a single POST.
func (c *Client) doOnce(ctx context.Context, method string, body []byte, out any, timeout time.Duration) error {
	reqCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.methodURL(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("rc: build request %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}

	started := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("rc: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, readErr := io.ReadAll(resp.Body)
	c.log.Debug("rc: call",
		"method", method,
		"status", resp.StatusCode,
		"duration", time.Since(started).String(),
	)
	if readErr != nil {
		return fmt.Errorf("rc: read %s response: %w", method, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return parseHTTPError(resp.StatusCode, data)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("rc: decode %s response: %w", method, err)
	}
	return nil
}

// encodeInput marshals the request body. nil becomes {} because rclone's RC
// endpoints expect a JSON object for POST requests.
func encodeInput(in any) ([]byte, error) {
	if in == nil {
		return []byte("{}"), nil
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("rc: encode request: %w", err)
	}
	if len(body) == 0 || string(body) == "null" {
		return []byte("{}"), nil
	}
	return body, nil
}

// parseHTTPError converts a non-2xx response into *Error. rclone normally
// returns JSON, but authentication failures are plain text.
func parseHTTPError(httpStatus int, body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var payload struct {
			Error  string         `json:"error"`
			Status int            `json:"status"`
			Path   string         `json:"path"`
			Input  map[string]any `json:"input"`
		}
		if err := json.Unmarshal(trimmed, &payload); err == nil && payload.Error != "" {
			status := payload.Status
			if status == 0 {
				status = httpStatus
			}
			return &Error{
				Status:  status,
				Path:    payload.Path,
				Message: payload.Error,
				Input:   payload.Input,
			}
		}
	}

	message := strings.TrimSpace(string(trimmed))
	if message == "" {
		message = http.StatusText(httpStatus)
	}
	return &Error{Status: httpStatus, Message: truncate(message, 4096)}
}

// isIdempotent reports whether a method is safe to retry automatically.
// Creating configs and starting/finishing jobs are never retried.
func isIdempotent(method string) bool {
	m := strings.TrimPrefix(strings.TrimSpace(method), "/")
	if m == "config/create" {
		return false
	}
	if strings.HasPrefix(m, "sync/") || strings.HasPrefix(m, "job/") {
		return false
	}
	return true
}

// isRetryable reports whether err came from the transport rather than from
// rclone itself. Context cancellation and deadline expiry are not retried.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var rcErr *Error
	if errors.As(err, &rcErr) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
