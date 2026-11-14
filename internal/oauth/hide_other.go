//go:build !windows

package oauth

import "os/exec"

func addHiddenWindow(cmd *exec.Cmd) {}
