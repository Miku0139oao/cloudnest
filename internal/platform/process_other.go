//go:build !windows

package platform

import "syscall"

// HideWindow is a no-op on non-Windows platforms.
func HideWindow(attr *syscall.SysProcAttr) {}
