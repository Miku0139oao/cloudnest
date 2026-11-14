package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cloudnest/internal/platform"
)

const (
	// MinVersionMajor, MinVersionMinor and MinVersionPatch define the oldest
	// supported rclone release (1.70.0).
	MinVersionMajor = 1
	MinVersionMinor = 70
	MinVersionPatch = 0

	versionTimeout = 5 * time.Second
)

var versionLineRE = regexp.MustCompile(`(?m)^rclone\s+v?(\d+)\.(\d+)\.(\d+)([^\r\n]*)`)

// VersionInfo is a parsed rclone version.
type VersionInfo struct {
	Major  int
	Minor  int
	Patch  int
	Suffix string
	Raw    string
}

// String returns the version in rclone's vMAJOR.MINOR.PATCH+suffix form.
func (v VersionInfo) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	return s + v.Suffix
}

// AtLeast reports whether v is at least major.minor.patch.
func (v VersionInfo) AtLeast(major, minor, patch int) bool {
	if v.Major != major {
		return v.Major > major
	}
	if v.Minor != minor {
		return v.Minor > minor
	}
	return v.Patch >= patch
}

// Version runs `bin version` with a five second timeout, parses the semver and
// enforces the 1.70.0 minimum. When the binary is valid but too old, the
// parsed VersionInfo is still returned alongside ErrVersionTooOld.
func Version(ctx context.Context, bin string) (VersionInfo, error) {
	if strings.TrimSpace(bin) == "" {
		return VersionInfo{}, fmt.Errorf("engine: empty rclone path")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()

	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	platform.HideWindow(cmd.SysProcAttr)
	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return VersionInfo{}, fmt.Errorf("engine: rclone version timed out: %w", ctxErr)
	}
	if err != nil {
		return VersionInfo{}, fmt.Errorf("engine: rclone version failed: %w: %s", err, truncateText(output.String(), 500))
	}

	info, err := parseVersionOutput(output.String())
	if err != nil {
		return VersionInfo{}, err
	}
	if !info.AtLeast(MinVersionMajor, MinVersionMinor, MinVersionPatch) {
		return info, fmt.Errorf("%w: found %s, need at least v%d.%d.%d",
			ErrVersionTooOld, info, MinVersionMajor, MinVersionMinor, MinVersionPatch)
	}
	return info, nil
}

// parseVersionOutput extracts the first `rclone vX.Y.Z` line. It is separate
// from Version so it can be tested without an executable.
func parseVersionOutput(output string) (VersionInfo, error) {
	m := versionLineRE.FindStringSubmatch(output)
	if m == nil {
		return VersionInfo{}, fmt.Errorf("engine: could not parse rclone version output: %q", truncateText(output, 200))
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	patch, err3 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return VersionInfo{}, fmt.Errorf("engine: invalid rclone version numbers %q", m[0])
	}
	return VersionInfo{
		Major:  major,
		Minor:  minor,
		Patch:  patch,
		Suffix: strings.TrimSpace(m[4]),
		Raw:    strings.TrimSpace(m[0]),
	}, nil
}

// FindBinary resolves the rclone executable in discovery order:
//
//  1. <AppRoot>\bin\rclone.exe (the portable bundle)
//  2. the first rclone found on PATH
//  3. %LOCALAPPDATA%\Cloudnest\bin\rclone.exe (the downloaded fallback)
//
// It only checks that the path exists; use ResolveBinary to also validate the
// version.
func FindBinary(appRoot string) (string, error) {
	for _, candidate := range binaryCandidates(appRoot) {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: searched %s", ErrNoBinary, strings.Join(binaryCandidates(appRoot), ", "))
}

// ResolveBinary finds an rclone executable and validates that it is at least
// 1.70.0. Candidates that exist but fail the version check are skipped so a
// newer binary later in the search order can still be used.
func ResolveBinary(ctx context.Context, appRoot string) (string, VersionInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	candidates := binaryCandidates(appRoot)
	found := false
	var lastErr error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return "", VersionInfo{}, err
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		found = true
		vi, err := Version(ctx, candidate)
		if err != nil {
			lastErr = err
			continue
		}
		return candidate, vi, nil
	}
	if !found {
		return "", VersionInfo{}, fmt.Errorf("%w: searched %s", ErrNoBinary, strings.Join(candidates, ", "))
	}
	if lastErr != nil {
		return "", VersionInfo{}, lastErr
	}
	return "", VersionInfo{}, ErrNoBinary
}

// binaryCandidates returns the de-duplicated discovery candidates in order.
func binaryCandidates(appRoot string) []string {
	name := binaryName()
	var candidates []string
	if appRoot != "" {
		candidates = append(candidates, filepath.Join(appRoot, "bin", name))
	}
	if path, err := exec.LookPath(name); err == nil && path != "" {
		candidates = append(candidates, path)
	}
	if local, err := platform.LocalAppDir(); err == nil {
		candidates = append(candidates, filepath.Join(local, "bin", name))
	}

	seen := make(map[string]struct{}, len(candidates))
	out := candidates[:0]
	for _, candidate := range candidates {
		key := strings.ToLower(filepath.Clean(candidate))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "rclone.exe"
	}
	return "rclone"
}

func truncateText(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
