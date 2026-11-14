//go:build windows

package platform

import "syscall"

// createNoWindow is CREATE_NO_WINDOW from the Windows process creation flags.
const createNoWindow = 0x08000000

// HideWindow configures attr so the child process does not allocate a console
// window. It is used for rclone and version probes.
func HideWindow(attr *syscall.SysProcAttr) {
	if attr == nil {
		return
	}
	attr.HideWindow = true
	attr.CreationFlags |= createNoWindow
}
