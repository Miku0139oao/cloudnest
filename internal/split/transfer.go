package split

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Hash reads src sequentially exactly once and fills m.Size, m.SHA256 and
// m.Parts (names, sizes and per-part SHA-256). m.PartCount/m.ChunkSize may be
// pre-set by the caller; invalid or missing values are repaired. onBytes, if
// non-nil, is called with the number of newly read bytes on each buffer.
func Hash(ctx context.Context, src string, m *Manifest, onBytes func(int64)) error {
	if m == nil {
		return errors.New("split: nil manifest")
	}
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("split: open source: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("split: stat source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("split: source is not a regular file: %s", src)
	}
	size := info.Size()
	if size <= 0 {
		return fmt.Errorf("split: source is empty: %s", src)
	}

	count := m.PartCount
	if count <= 0 {
		if m.ChunkSize > 0 {
			n := ceilDiv(size, m.ChunkSize)
			if n > maxParts {
				return fmt.Errorf("split: would require %d parts (max %d)", n, maxParts)
			}
			count = int(n)
		} else {
			count = PartCount(size, DefaultOptions())
		}
	}
	if count < 1 || count > maxParts {
		return fmt.Errorf("split: invalid part count %d", count)
	}
	if size < int64(count) {
		return fmt.Errorf("split: source is too small to split into %d parts", count)
	}

	balanced := ChunkSize(size, count)
	if balanced <= 0 {
		return errors.New("split: cannot calculate chunk size")
	}
	chunk := m.ChunkSize
	if chunk <= 0 || chunk < balanced {
		chunk = balanced
	} else if count > 1 {
		// Avoid a zero-length final part when a caller supplied an
		// oversized chunk size.
		if chunk >= ceilDiv(size, int64(count-1)) {
			chunk = balanced
		}
	}

	whole := sha256.New()
	parts := make([]PartInfo, 0, count)
	buf := make([]byte, maxIOBuffer)
	var consumed int64

	for i := 0; i < count; i++ {
		left := size - consumed
		remainingParts := int64(count - i)
		partSize := chunk
		if partSize > left {
			partSize = left
		}
		if i == count-1 {
			partSize = left
		}
		// The 1 MiB alignment can make chunk larger than a tiny file. Keep
		// at least one byte for every remaining part instead of producing a
		// zero-length part.
		if left-partSize < remainingParts-1 {
			partSize = left - (remainingParts - 1)
		}
		if partSize <= 0 {
			return fmt.Errorf("split: computed empty part %d", i+1)
		}

		partHash := sha256.New()
		var got int64
		for got < partSize {
			if err := ctx.Err(); err != nil {
				return err
			}
			want := int64(len(buf))
			if remaining := partSize - got; remaining < want {
				want = remaining
			}
			n, readErr := io.ReadFull(f, buf[:want])
			if n > 0 {
				_, _ = whole.Write(buf[:n])
				_, _ = partHash.Write(buf[:n])
				got += int64(n)
				if onBytes != nil {
					onBytes(int64(n))
				}
			}
			if readErr != nil {
				return fmt.Errorf("split: read source at part %d: %w", i+1, readErr)
			}
		}

		parts = append(parts, PartInfo{
			Index:  i + 1,
			Name:   partName(i + 1),
			Size:   partSize,
			SHA256: hex.EncodeToString(partHash.Sum(nil)),
		})
		consumed += partSize
	}

	var extra [1]byte
	if n, readErr := f.Read(extra[:]); n > 0 {
		return errors.New("split: source changed while hashing")
	} else if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("split: read source after final part: %w", readErr)
	}
	if consumed != size {
		return fmt.Errorf("split: hashed %d of %d bytes", consumed, size)
	}

	m.Size = size
	m.ChunkSize = chunk
	m.PartCount = count
	m.Parts = parts
	m.SHA256 = hex.EncodeToString(whole.Sum(nil))
	return nil
}

// Stage writes only the requested part indexes into dir. The source is opened
// once and read with ReadAt, so staging several parts does not re-read the
// whole file for each part. onBytes receives newly staged byte counts.
func Stage(ctx context.Context, src string, m Manifest, indexes []int, dir string, onBytes func(int64)) error {
	if len(indexes) == 0 {
		return nil
	}
	if err := validatePartLayout(m); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("split: open source: %w", err)
	}
	defer srcFile.Close()
	info, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("split: stat source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != m.Size {
		return fmt.Errorf("split: source size/mode changed since hashing")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("split: create parts directory: %w", err)
	}

	offsets := make([]int64, len(m.Parts))
	var off int64
	for i, p := range m.Parts {
		offsets[i] = off
		off += p.Size
	}

	seen := make(map[int]struct{}, len(indexes))
	buf := make([]byte, maxIOBuffer)
	for _, index := range indexes {
		if index < 1 || index > len(m.Parts) {
			return fmt.Errorf("split: part index %d out of range", index)
		}
		if _, ok := seen[index]; ok {
			return fmt.Errorf("split: duplicate part index %d", index)
		}
		seen[index] = struct{}{}

		p := m.Parts[index-1]
		if err := stageOne(ctx, srcFile, offsets[index-1], p, dir, buf, onBytes); err != nil {
			return err
		}
	}
	return nil
}

func stageOne(ctx context.Context, src *os.File, offset int64, p PartInfo, dir string, buf []byte, onBytes func(int64)) error {
	tmp, err := os.CreateTemp(dir, "."+p.Name+".*.tmp")
	if err != nil {
		return fmt.Errorf("split: create temp for part %d: %w", p.Index, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("split: set permissions for part %d: %w", p.Index, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	partHash := sha256.New()
	var got int64
	for got < p.Size {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buf))
		if remaining := p.Size - got; remaining < want {
			want = remaining
		}
		n, readErr := src.ReadAt(buf[:want], offset+got)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				return fmt.Errorf("split: write part %d: %w", p.Index, err)
			}
			_, _ = partHash.Write(buf[:n])
			got += int64(n)
			if onBytes != nil {
				onBytes(int64(n))
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) && got == p.Size {
				break
			}
			return fmt.Errorf("split: read part %d from source: %w", p.Index, readErr)
		}
		if n == 0 {
			return fmt.Errorf("split: unexpected EOF in part %d", p.Index)
		}
	}

	if !strings.EqualFold(hex.EncodeToString(partHash.Sum(nil)), p.SHA256) {
		return fmt.Errorf("split: SHA-256 mismatch for part %d", p.Index)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("split: sync part %d: %w", p.Index, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("split: close part %d: %w", p.Index, err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, p.Name)); err != nil {
		return fmt.Errorf("split: install part %d: %w", p.Index, err)
	}
	committed = true
	return nil
}

// VerifyDir checks the requested part indexes (all parts when indexes is nil
// or empty) for the expected size and SHA-256. Bad indexes are returned in
// bad; err reports only operational failures such as cancellation.
func VerifyDir(ctx context.Context, dir string, m Manifest, indexes []int) (bad []int, err error) {
	if err := validatePartLayout(m); err != nil {
		return nil, err
	}
	if len(indexes) == 0 {
		indexes = make([]int, 0, len(m.Parts))
		for i := range m.Parts {
			indexes = append(indexes, i+1)
		}
	}

	seen := make(map[int]struct{}, len(indexes))
	buf := make([]byte, maxIOBuffer)
	for _, index := range indexes {
		if index < 1 || index > len(m.Parts) {
			return bad, fmt.Errorf("split: part index %d out of range", index)
		}
		if _, ok := seen[index]; ok {
			continue
		}
		seen[index] = struct{}{}

		p := m.Parts[index-1]
		path := filepath.Join(dir, p.Name)
		f, err := os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				bad = append(bad, index)
				continue
			}
			return bad, fmt.Errorf("split: open part %d: %w", index, err)
		}

		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return bad, fmt.Errorf("split: stat part %d: %w", index, err)
		}
		if !st.Mode().IsRegular() || st.Size() != p.Size {
			_ = f.Close()
			bad = append(bad, index)
			continue
		}

		h := sha256.New()
		good := true
		for {
			if err := ctx.Err(); err != nil {
				_ = f.Close()
				return bad, err
			}
			n, readErr := f.Read(buf)
			if n > 0 {
				_, _ = h.Write(buf[:n])
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				_ = f.Close()
				return bad, fmt.Errorf("split: verify part %d: %w", index, readErr)
			}
		}
		if err := f.Close(); err != nil {
			return bad, fmt.Errorf("split: close part %d: %w", index, err)
		}
		if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), p.SHA256) {
			good = false
		}
		if !good {
			bad = append(bad, index)
		}
	}
	return bad, nil
}

// Merge verifies each part while streaming it into <dst>.cloudbox-partial,
// computes the whole-file SHA-256, compares it with the manifest, and only then
// renames the partial file into place. On any failure the partial file is
// removed and the destination is not created or replaced by this call.
// onBytes receives newly written byte counts.
func Merge(ctx context.Context, partsDir, dstPath string, m Manifest, onBytes func(int64)) (sha string, err error) {
	if err := ValidateManifest(m); err != nil {
		return "", err
	}
	if dstPath == "" {
		return "", errors.New("split: empty destination path")
	}

	partialPath := dstPath + ".cloudbox-partial"
	out, err := os.OpenFile(partialPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("split: create partial destination: %w", err)
	}
	committed := false
	defer func() {
		_ = out.Close()
		if !committed {
			_ = os.Remove(partialPath)
		}
	}()

	buf := make([]byte, maxIOBuffer)
	whole := sha256.New()
	for _, p := range m.Parts {
		if err := mergeOne(ctx, out, filepath.Join(partsDir, p.Name), p, whole, buf, onBytes); err != nil {
			return "", err
		}
	}

	got := hex.EncodeToString(whole.Sum(nil))
	if !strings.EqualFold(got, m.SHA256) {
		return "", fmt.Errorf("split: whole-file SHA-256 mismatch")
	}
	if err := out.Sync(); err != nil {
		return "", fmt.Errorf("split: sync destination: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("split: close destination: %w", err)
	}
	if err := os.Rename(partialPath, dstPath); err != nil {
		return "", fmt.Errorf("split: install destination: %w", err)
	}
	committed = true
	return got, nil
}

func mergeOne(ctx context.Context, out *os.File, path string, p PartInfo, whole io.Writer, buf []byte, onBytes func(int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("split: open part %d: %w", p.Index, err)
	}
	defer f.Close()

	partHash := sha256.New()
	var got int64
	for got < p.Size {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buf))
		if remaining := p.Size - got; remaining < want {
			want = remaining
		}
		n, readErr := f.Read(buf[:want])
		if n > 0 {
			if err := writeFull(out, buf[:n]); err != nil {
				return fmt.Errorf("split: write destination for part %d: %w", p.Index, err)
			}
			_, _ = partHash.Write(buf[:n])
			_, _ = whole.Write(buf[:n])
			got += int64(n)
			if onBytes != nil {
				onBytes(int64(n))
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) && got == p.Size {
				break
			}
			return fmt.Errorf("split: read part %d: %w", p.Index, readErr)
		}
		if n == 0 {
			return fmt.Errorf("split: unexpected EOF in part %d", p.Index)
		}
	}

	var extra [1]byte
	if n, readErr := f.Read(extra[:]); n > 0 {
		return fmt.Errorf("split: part %d is larger than the manifest", p.Index)
	} else if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("split: read part %d: %w", p.Index, readErr)
	}
	if !strings.EqualFold(hex.EncodeToString(partHash.Sum(nil)), p.SHA256) {
		return fmt.Errorf("split: SHA-256 mismatch for part %d", p.Index)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("split: close part %d: %w", p.Index, err)
	}
	return nil
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func ceilDiv(n, d int64) int64 {
	q := n / d
	if n%d != 0 {
		q++
	}
	return q
}
