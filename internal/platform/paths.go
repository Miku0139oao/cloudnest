// Package platform contains the small, Windows-focused helpers Cloudnest
// needs for portable storage, process spawning and shell integration.
//
// Everything in this package is deliberately dependency free: only the Go
// standard library is used. Windows specific files use the _windows.go
// suffix; matching _other.go stubs keep the package buildable on other
// platforms (Cloudnest only ships Windows builds, but keeping the build tags
// honest makes tooling and tests painless).
package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrUnsupported is returned by helpers which have no implementation on the
// current platform.
var ErrUnsupported = errors.New("platform: operation not supported on this platform")

// probeMu serialises the writability probe in DataDir. It is only held for a
// few file operations.
var probeMu sync.Mutex

// AppRoot returns the directory containing the running executable. For a
// portable Cloudnest build this is the folder the user unpacked the ZIP into.
func AppRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("platform: locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if dir == "" || dir == "." {
		return "", errors.New("platform: could not determine application directory")
	}
	return dir, nil
}

// LocalAppDir returns %LOCALAPPDATA%\Cloudnest. When LOCALAPPDATA is not set
// (for example on non-Windows builds) it falls back to the OS user config
// directory.
func LocalAppDir() (string, error) {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return filepath.Join(v, "Cloudnest"), nil
	}
	if v, err := os.UserConfigDir(); err == nil && v != "" {
		return filepath.Join(v, "Cloudnest"), nil
	}
	return "", errors.New("platform: LOCALAPPDATA is not set")
}

// DataDir returns the directory Cloudnest stores mutable data in.
//
// It prefers <AppRoot>\data so a portable copy keeps everything in one folder.
// The candidate is probed by creating a .probe file; if the application folder
// is not writable (for example when installed under Program Files) the
// fallback %LOCALAPPDATA%\Cloudnest is created and returned instead.
func DataDir() (string, error) {
	probeMu.Lock()
	defer probeMu.Unlock()

	var rootErr error
	if root, err := AppRoot(); err == nil {
		candidate := filepath.Join(root, "data")
		if mkErr := os.MkdirAll(candidate, 0o755); mkErr == nil {
			probe := filepath.Join(candidate, ".probe")
			f, openErr := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if openErr == nil {
				_, writeErr := f.WriteString("probe")
				closeErr := f.Close()
				_ = os.Remove(probe)
				if writeErr == nil && closeErr == nil {
					return candidate, nil
				}
			}
		}
	} else {
		rootErr = err
	}

	fallback, err := LocalAppDir()
	if err != nil {
		if rootErr != nil {
			return "", fmt.Errorf("platform: data directory: %v (fallback: %w)", rootErr, err)
		}
		return "", fmt.Errorf("platform: data directory: %w", err)
	}
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		return "", fmt.Errorf("platform: create fallback data directory: %w", err)
	}
	return fallback, nil
}

// BinDir returns <AppRoot>\bin, where the bundled rclone.exe lives.
func BinDir() (string, error) {
	root, err := AppRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin"), nil
}

// LogDir returns the directory Cloudnest writes its own logs to.
func LogDir() (string, error) {
	data, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "logs"), nil
}

// StagingDir returns the directory used for temporary split/merge work.
func StagingDir() (string, error) {
	data, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "staging"), nil
}

// EnsureDirs creates the mutable data directories Cloudnest needs: DataDir,
// LogDir and StagingDir. The bundled bin directory is part of the portable
// distribution and is not created here (it may live in a read-only folder).
func EnsureDirs() error {
	data, err := DataDir()
	if err != nil {
		return err
	}
	dirs := []string{
		data,
		filepath.Join(data, "logs"),
		filepath.Join(data, "staging"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("platform: create %s: %w", dir, err)
		}
	}
	return nil
}
