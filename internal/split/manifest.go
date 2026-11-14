package split

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
)

const (
	maxManifestBytes = 64 * 1024
	maxIOBuffer      = 1 << 20
)

var (
	partNamePattern     = regexp.MustCompile(`^part-\d{4}\.bin$`)
	errManifestTooLarge = errors.New("split: manifest is too large")
)

// ValidateManifest performs strict validation of the on-disk manifest. It
// intentionally rejects structurally inconsistent data before Merge can touch
// the destination.
func ValidateManifest(m Manifest) error {
	if m.Format != Format {
		return fmt.Errorf("split: unsupported manifest format %q", m.Format)
	}
	if m.Size <= 0 {
		return errors.New("split: manifest size must be positive")
	}
	if m.PartCount < 1 || m.PartCount > maxParts {
		return fmt.Errorf("split: invalid part count %d", m.PartCount)
	}
	if m.PartCount != len(m.Parts) {
		return fmt.Errorf("split: part count %d does not match %d parts", m.PartCount, len(m.Parts))
	}
	if m.ChunkSize <= 0 {
		return errors.New("split: chunk size must be positive")
	}
	if !isSHA256Hex(m.SHA256) {
		return fmt.Errorf("split: invalid whole-file SHA-256 %q", m.SHA256)
	}

	var total int64
	for i := range m.Parts {
		p := m.Parts[i]
		wantIndex := i + 1
		if p.Index != wantIndex {
			return fmt.Errorf("split: part %d has index %d, want %d", i, p.Index, wantIndex)
		}
		if !partNamePattern.MatchString(p.Name) {
			return fmt.Errorf("split: invalid part name %q", p.Name)
		}
		if p.Name != partName(wantIndex) {
			return fmt.Errorf("split: part %d has name %q, want %q", wantIndex, p.Name, partName(wantIndex))
		}
		if p.Size <= 0 {
			return fmt.Errorf("split: part %d has non-positive size %d", wantIndex, p.Size)
		}
		if total > math.MaxInt64-p.Size {
			return errors.New("split: part sizes overflow int64")
		}
		total += p.Size
		if !isSHA256Hex(p.SHA256) {
			return fmt.Errorf("split: invalid SHA-256 for part %d", wantIndex)
		}
	}
	if total != m.Size {
		return fmt.Errorf("split: part sizes sum to %d, manifest size is %d", total, m.Size)
	}

	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("split: encode manifest: %w", err)
	}
	if len(data) > maxManifestBytes {
		return fmt.Errorf("split: manifest is too large (%d bytes, max %d)", len(data), maxManifestBytes)
	}
	return nil
}

func isSHA256Hex(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func partName(index int) string {
	return fmt.Sprintf("part-%04d.bin", index)
}

// validatePartLayout checks the fields needed by Stage, VerifyDir and Merge.
// It does not require the presentation-only fields (format, name, timestamps).
func validatePartLayout(m Manifest) error {
	if m.Size <= 0 {
		return errors.New("split: manifest size must be positive")
	}
	if m.PartCount < 1 || m.PartCount > maxParts {
		return fmt.Errorf("split: invalid part count %d", m.PartCount)
	}
	if m.PartCount != len(m.Parts) {
		return fmt.Errorf("split: part count %d does not match %d parts", m.PartCount, len(m.Parts))
	}
	var total int64
	for i, p := range m.Parts {
		want := i + 1
		if p.Index != want {
			return fmt.Errorf("split: part %d has index %d, want %d", i, p.Index, want)
		}
		if p.Name != partName(want) {
			return fmt.Errorf("split: part %d has name %q, want %q", want, p.Name, partName(want))
		}
		if p.Size <= 0 {
			return fmt.Errorf("split: part %d has non-positive size", want)
		}
		if total > math.MaxInt64-p.Size {
			return errors.New("split: part sizes overflow int64")
		}
		total += p.Size
		if !isSHA256Hex(p.SHA256) {
			return fmt.Errorf("split: invalid SHA-256 for part %d", want)
		}
	}
	if total != m.Size {
		return fmt.Errorf("split: part sizes sum to %d, manifest size is %d", total, m.Size)
	}
	return nil
}

// WriteMeta writes README.txt first and then atomically replaces manifest.json.
// Writing the manifest last preserves its role as the completion marker.
func WriteMeta(dir string, m Manifest, readme string) error {
	if err := ValidateManifest(m); err != nil {
		return fmt.Errorf("split: refusing to write invalid manifest: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("split: create parts directory: %w", err)
	}
	if readme == "" {
		readme = Readme(m)
	}
	if err := writeFileAtomic(dir, "README.txt", []byte(readme), 0o644); err != nil {
		return err
	}

	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("split: encode manifest: %w", err)
	}
	if len(data) > maxManifestBytes {
		return fmt.Errorf("split: manifest is too large (%d bytes, max %d)", len(data), maxManifestBytes)
	}
	if err := writeFileAtomic(dir, "manifest.json", data, 0o644); err != nil {
		return err
	}
	return nil
}

func writeFileAtomic(dir, name string, data []byte, perm fs.FileMode) error {
	f, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return fmt.Errorf("split: create temporary %s: %w", name, err)
	}
	tmp := f.Name()
	committed := false
	defer func() {
		_ = f.Close()
		if !committed {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("split: write %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("split: sync %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("split: close %s: %w", name, err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return fmt.Errorf("split: chmod %s: %w", name, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("split: install %s: %w", name, err)
	}
	committed = true
	return nil
}

// ReadManifestFile reads, strictly decodes and validates manifest.json.
func ReadManifestFile(dir string) (Manifest, error) {
	path := filepath.Join(dir, "manifest.json")
	data, err := readManifestBytes(path)
	if err != nil {
		return Manifest{}, err
	}

	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("split: decode %s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("split: trailing data in %s", path)
		}
		return Manifest{}, fmt.Errorf("split: decode %s: %w", path, err)
	}
	if err := ValidateManifest(m); err != nil {
		return Manifest{}, fmt.Errorf("split: invalid %s: %w", path, err)
	}
	return m, nil
}

func readManifestBytes(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("split: read %s: %w", path, err)
	}
	if len(data) > maxManifestBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes (max %d)", errManifestTooLarge, path, len(data), maxManifestBytes)
	}
	return data, nil
}

// InspectDir inspects the local layout. It does not hash part contents; use
// VerifyDir for content verification. A missing manifest means "incomplete",
// a manifest from another format means "foreign", and a missing/mis-sized part
// means "corrupt".
func InspectDir(dir string) (SetInfo, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return SetInfo{State: SetAbsent}, nil
	}
	if err != nil {
		return SetInfo{}, fmt.Errorf("split: inspect %s: %w", dir, err)
	}
	if !info.IsDir() {
		return SetInfo{State: SetForeign, Foreign: true}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return SetInfo{}, fmt.Errorf("split: read %s: %w", dir, err)
	}

	path := filepath.Join(dir, "manifest.json")
	data, err := readManifestBytes(path)
	if errors.Is(err, fs.ErrNotExist) {
		return SetInfo{
			State:      SetIncomplete,
			PartsFound: countPartFiles(entries),
		}, nil
	}
	if err != nil {
		if errors.Is(err, errManifestTooLarge) {
			return SetInfo{State: SetCorrupt}, nil
		}
		return SetInfo{}, err
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return SetInfo{State: SetCorrupt}, nil
	}
	if m.Format != Format {
		return SetInfo{State: SetForeign, Manifest: &m, Foreign: true}, nil
	}
	if err := ValidateManifest(m); err != nil {
		return SetInfo{State: SetCorrupt, Manifest: &m}, nil
	}

	var missing []int
	found := 0
	corrupt := false
	for _, p := range m.Parts {
		st, err := os.Stat(filepath.Join(dir, p.Name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				missing = append(missing, p.Index)
				continue
			}
			return SetInfo{}, fmt.Errorf("split: inspect part %s: %w", p.Name, err)
		}
		found++
		if !st.Mode().IsRegular() || st.Size() != p.Size {
			corrupt = true
		}
	}

	state := SetComplete
	if len(missing) > 0 || corrupt {
		state = SetCorrupt
	}
	return SetInfo{
		State:      state,
		Manifest:   &m,
		PartsFound: found,
		Missing:    missing,
	}, nil
}

func countPartFiles(entries []fs.DirEntry) int {
	n := 0
	for _, entry := range entries {
		if !entry.IsDir() && partNamePattern.MatchString(entry.Name()) {
			n++
		}
	}
	return n
}
