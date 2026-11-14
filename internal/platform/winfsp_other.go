//go:build !windows

package platform

// WinFspInstalled reports whether the WinFsp filesystem driver is available.
// WinFsp is Windows-only, so non-Windows builds are treated as ready.
func WinFspInstalled() bool { return true }
