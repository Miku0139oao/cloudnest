//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// WinFspInstalled reports whether the WinFsp filesystem driver is available.
//
// rclone's Windows mount support needs the WinFsp driver. The installer records
// its location under one of two registry views; if neither key is readable (for
// example under a restricted account) the DLL shipped by the installer is used
// as a fallback.
func WinFspInstalled() bool {
	for _, name := range []string{
		`SOFTWARE\WOW6432Node\WinFsp`,
		`SOFTWARE\WinFsp`,
	} {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		installDir, _, valueErr := key.GetStringValue("InstallDir")
		key.Close()
		if valueErr == nil && strings.TrimSpace(installDir) != "" {
			return true
		}
	}

	if dir := strings.TrimSpace(os.Getenv("ProgramFiles(x86)")); dir != "" {
		dll := filepath.Join(dir, "WinFsp", "bin", "winfsp-x64.dll")
		if _, err := os.Stat(dll); err == nil {
			return true
		}
	}
	return false
}
