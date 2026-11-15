//go:build !windows && !linux && !android && !darwin && !freebsd && !dragonfly && !openbsd && !solaris

package platform

// FreeBytes is implemented on Windows, Linux, Android, macOS and the BSDs that
// expose statfs/statvfs through golang.org/x/sys/unix. Remaining targets
// (netbsd, aix, illumos, plan9, js, wasip1) get a hard error: split uploads
// must not stage a large file when free space cannot be checked.
func FreeBytes(path string) (uint64, error) {
	return 0, ErrUnsupported
}
