//go:build !windows

package platform

// FreeBytes is only implemented on Windows.
func FreeBytes(path string) (uint64, error) {
	return 0, ErrUnsupported
}
