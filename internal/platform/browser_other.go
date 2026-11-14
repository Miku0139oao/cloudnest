//go:build !windows

package platform

// OpenURL is only implemented on Windows.
func OpenURL(rawURL string) error {
	return ErrUnsupported
}
