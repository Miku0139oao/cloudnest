//go:build !windows

package files

import "errors"

func knownFolder(id string) (string, error) {
	return "", errors.New("known folders are only implemented on Windows")
}
