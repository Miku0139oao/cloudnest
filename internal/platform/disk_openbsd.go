//go:build openbsd

package platform

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// FreeBytes reports the number of free bytes available to the current user on
// the filesystem containing path. OpenBSD spells the statfs fields differently
// from the other BSDs.
func FreeBytes(path string) (uint64, error) {
	if path == "" {
		return 0, fmt.Errorf("platform: FreeBytes: empty path")
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("platform: statfs(%q): %w", path, err)
	}
	if st.F_bavail < 0 {
		return 0, nil
	}
	avail := uint64(st.F_bavail)
	bsize := uint64(st.F_bsize)
	if bsize != 0 && avail > ^uint64(0)/bsize {
		return ^uint64(0), nil
	}
	return avail * bsize, nil
}
