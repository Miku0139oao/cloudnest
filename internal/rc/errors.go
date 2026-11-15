package rc

import (
	"errors"
	"fmt"
	"strings"
)

// Error is an error returned by the rclone Remote Control API.
//
// rclone responds to a failed call with a JSON object of the form
//
//	{"error": "...", "status": 404, "path": "operations/list", "input": {...}}
//
// and answers unauthenticated requests with a plain-text 401. Error keeps the
// status and the request path so callers can classify failures without
// parsing strings. Input is intentionally not included in Error's string
// representation because config calls can contain secrets.
type Error struct {
	Status  int
	Path    string
	Message string
	Input   map[string]any
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "rclone rc: <nil>"
	}
	status := e.Status
	if status == 0 {
		if e.Path != "" {
			return fmt.Sprintf("rclone rc %s: %s", e.Path, e.Message)
		}
		return fmt.Sprintf("rclone rc: %s", e.Message)
	}
	if e.Path != "" {
		return fmt.Sprintf("rclone rc %s: %d: %s", e.Path, status, e.Message)
	}
	return fmt.Sprintf("rclone rc: %d: %s", status, e.Message)
}

// IsAuth reports whether err indicates an authentication or authorisation
// failure (HTTP 401/403).
func IsAuth(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	if e.Status == 401 || e.Status == 403 {
		return true
	}
	msg := strings.ToLower(e.Message)
	return strings.Contains(msg, "unauthorized") || strings.Contains(msg, "forbidden")
}

// IsNotFound reports whether err indicates that the remote, object or RC
// method does not exist.
func IsNotFound(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	if e.Status == 404 {
		return true
	}
	msg := strings.ToLower(e.Message)
	return strings.Contains(msg, "not found") || strings.Contains(msg, "couldn't find method")
}

// IsMethodMissing reports whether err says the running rclone does not
// implement the requested RC method. rclone answers an unknown method with a
// 404 whose message is `couldn't find method "..."`, the same status it uses for
// a missing object, so callers which treat 404 as "the file is absent" must
// rule this out first.
func IsMethodMissing(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	msg := strings.ToLower(e.Message)
	return strings.Contains(msg, "couldn't find method") || strings.Contains(msg, "didn't find method")
}
