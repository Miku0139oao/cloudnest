//go:build solaris

package platform

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// FreeBytes reports the number of free bytes available to the current user on
// the filesystem containing path. Solaris (and not illumos, which x/sys does
// not give statvfs for) uses statvfs rather than statfs.
func FreeBytes(path string) (uint64, error) {
	if path == "" {
		return 0, fmt.Errorf("platform: FreeBytes: empty path")
	}
	var st unix.Statvfs_t
	if err := unix.Statvfs(path, &st); err != nil {
		return 0, fmt.Errorf("platform: statvfs(%q): %w", path, err)
	}
	// Frsize is the fundamental block size; Bsize is the preferred block size.
	bsize := st.Frsize
	if bsize == 0 {
		bsize = st.Bsize
	}
	avail := st.Bavail
	if bsize != 0 && avail > ^uint64(0)/bsize {
		return ^uint64(0), nil
	}
	return avail * bsize, nil
}
