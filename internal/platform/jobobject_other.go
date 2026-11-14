//go:build !windows

package platform

// JobObject is a no-op stand-in on non-Windows platforms.
type JobObject struct{}

// NewJobObject returns a no-op job object.
func NewJobObject() (*JobObject, error) { return &JobObject{}, nil }

// Assign is a no-op on non-Windows platforms.
func (j *JobObject) Assign(pid int) error { return nil }

// Close is a no-op on non-Windows platforms.
func (j *JobObject) Close() error { return nil }
