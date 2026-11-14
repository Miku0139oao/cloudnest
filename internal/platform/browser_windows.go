//go:build windows

package platform

import (
	"fmt"
	"net/url"
	"strings"
	"syscall"
	"unsafe"
)

var (
	shell32                  = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW        = shell32.NewProc("ShellExecuteW")
	swShowNormal      uint32 = 1
)

// OpenURL opens rawURL in the user's default browser without spawning a
// console window. Only http and https URLs are accepted so a malformed OAuth
// response cannot ask the shell to launch an arbitrary handler.
func OpenURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("platform: OpenURL: empty URL")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("platform: OpenURL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("platform: OpenURL: refusing to open scheme %q", u.Scheme)
	}

	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return fmt.Errorf("platform: OpenURL: %w", err)
	}
	target, err := syscall.UTF16PtrFromString(rawURL)
	if err != nil {
		return fmt.Errorf("platform: OpenURL: %w", err)
	}

	r1, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0,
		0,
		uintptr(swShowNormal),
	)
	// ShellExecuteW returns a value greater than 32 on success. On failure it
	// returns an SE_ERR_* code; the last-error value is usually not useful.
	if r1 <= 32 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("platform: OpenURL: %w", callErr)
		}
		return fmt.Errorf("platform: OpenURL: ShellExecuteW failed with code %d", r1)
	}
	return nil
}
