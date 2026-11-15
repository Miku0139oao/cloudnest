//go:build linux || android || darwin || freebsd || dragonfly

package platform

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// FreeBytes reports the number of free bytes available to the current user on
// the filesystem containing path. Split uploads stage a whole large file on
// disk first, so the same pre-flight check Windows gets applies here.
func FreeBytes(path string) (uint64, error) {
	if path == "" {
		return 0, fmt.Errorf("platform: FreeBytes: empty path")
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("platform: statfs(%q): %w", path, err)
	}
	avail := uint64(st.Bavail)
	bsize := uint64(st.Bsize)
	if bsize != 0 && avail > ^uint64(0)/bsize {
		return ^uint64(0), nil
	}
	return avail * bsize, nil
}
