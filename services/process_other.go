//go:build !windows

package services

import "os/exec"

func addHiddenWindow(cmd *exec.Cmd) {}
