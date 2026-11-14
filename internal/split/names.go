package split

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxSanitizedBytes = 240

// SanitizeName keeps only the final path component, rejects traversal and
// Windows-illegal names, trims trailing dots/spaces, and truncates overly long
// names to 240 UTF-8 bytes (239 bytes plus a trailing '~').
func SanitizeName(name string) (string, error) {
	if name == "" {
		return "", errors.New("split: empty file name")
	}

	// Reject traversal components before reducing to the last component. This
	// makes "..\\evil" invalid rather than silently accepting "evil".
	for _, component := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if component == ".." {
			return "", fmt.Errorf("split: file name contains path traversal: %q", name)
		}
	}

	base := name
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("split: invalid file name: %q", name)
	}

	for _, r := range base {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("split: file name contains a control character: %q", name)
		}
	}
	if hasWindowsIllegal(base) {
		return "", fmt.Errorf("split: file name contains a Windows-illegal character: %q", name)
	}

	base = strings.TrimRight(base, " .")
	if base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("split: invalid file name: %q", name)
	}
	if isReservedDeviceName(base) {
		return "", fmt.Errorf("split: reserved Windows device name: %q", name)
	}

	if len(base) > maxSanitizedBytes {
		base = truncateUTF8(base, maxSanitizedBytes-1) + "~"
		base = strings.TrimRight(base, " .")
		if base == "" {
			return "", fmt.Errorf("split: invalid file name: %q", name)
		}
	}
	return base, nil
}

func hasWindowsIllegal(s string) bool {
	return strings.ContainsAny(s, `<>:"|?*/\`)
}

func isReservedDeviceName(s string) bool {
	stem := s
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.TrimRight(stem, " .")
	upper := strings.ToUpper(stem)
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(upper) == 4 &&
		(strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) &&
		upper[3] >= '1' && upper[3] <= '9' {
		return true
	}
	return false
}

func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	b := []byte(s)[:max]
	for !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}

// FindConflicts suggests "name (1).ext", "name (2).ext", ... until an unused
// path is found. The boolean reports whether the originally requested name was
// already present.
func FindConflicts(destDir, name string) (newName string, exists bool) {
	if _, err := os.Stat(filepath.Join(destDir, name)); errors.Is(err, fs.ErrNotExist) {
		return name, false
	}

	for i := 1; i <= 10000; i++ {
		candidate := addConflictSuffix(name, i)
		_, err := os.Stat(filepath.Join(destDir, candidate))
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, true
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Treat an unreadable destination as occupied. Continuing would
			// just generate more paths we cannot verify.
			return candidate, true
		}
	}
	return addConflictSuffix(name, 10001), true
}

func addConflictSuffix(name string, n int) string {
	ext := filepath.Ext(name)
	if ext == name { // ".gitignore" is a name, not an extension.
		ext = ""
	}
	base := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s (%d)%s", base, n, ext)
}
