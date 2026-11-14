//go:build windows

package services

import (
	"os/exec"
	"syscall"

	"cloudnest/internal/platform"
)

func addHiddenWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	platform.HideWindow(cmd.SysProcAttr)
}
