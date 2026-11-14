//go:build windows

package platform

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// FreeBytes reports the number of free bytes available to the current user on
// the volume containing path. It uses GetDiskFreeSpaceExW so it works for UNC
// paths and volumes larger than 2^32 bytes.
func FreeBytes(path string) (uint64, error) {
	if path == "" {
		return 0, fmt.Errorf("platform: FreeBytes: empty path")
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("platform: FreeBytes: %w", err)
	}

	var freeAvailable, total, totalFree uint64
	r1, _, callErr := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r1 == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return 0, fmt.Errorf("platform: GetDiskFreeSpaceExW(%q): %w", path, callErr)
		}
		return 0, fmt.Errorf("platform: GetDiskFreeSpaceExW(%q) failed", path)
	}
	return freeAvailable, nil
}
