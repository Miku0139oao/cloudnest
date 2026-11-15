// Package planner computes a preflight estimate for an rclone-style backup.
//
// It walks a local source tree, counts the files and bytes that would be
// uploaded, and in "mirror" mode compares the local tree against a remote
// listing to report the files that a sync would delete. The resulting counts
// are bound to a short-lived HMAC token so a later job can prove that the
// destructive mirror plan it executes is the same one the user approved.
//
// The package is deliberately transport independent: the caller supplies the
// walker and the remote lister. Only the Go standard library is used.
package planner

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloudnest/internal/dto"
)

// Backup modes accepted by Request.Mode.
const (
	ModeAdditive = "additive"
	ModeMirror   = "mirror"
)

const (
	// DefaultMaxEntries is the deletion limit used when Request.MaxEntries is
	// zero or negative.
	DefaultMaxEntries = 100000

	// DefaultTokenTTL is used when Request.TokenTTL is zero. Negative values
	// are honoured, which lets a caller mint an already-expired token.
	DefaultTokenTTL = 5 * time.Minute

	// maxReportedDeletions bounds the Returned Deletions slice so a plan can
	// never produce an unbounded UI payload. DeleteCount/DeleteBytes still
	// describe every deletion.
	maxReportedDeletions = 200
)

// Token verification failures. Use errors.Is to distinguish them.
var (
	ErrInvalidToken = errors.New("planner: invalid plan token")
	ErrTokenExpired = errors.New("planner: plan token expired")
)

// LocalFile is one regular file found below the source root. Path is relative
// to the root and uses forward slashes.
type LocalFile struct {
	Path string
	Size int64
}

// LocalWalkFunc walks root and calls fn for every regular file. Implementations
// must not follow symlinks or reparse points and should check ctx periodically.
type LocalWalkFunc func(ctx context.Context, root string, fn func(LocalFile) error) error

// RemoteLister lists the remote destination subtree. ListRecursive returns
// files (not directories) below remotePath, with paths relative to remotePath.
type RemoteLister interface {
	ListRecursive(ctx context.Context, remotePath string) ([]dto.Entry, error)
}

// Request describes one preflight computation.
type Request struct {
	LocalRoot   string
	RemotePath  string
	Mode        string // "additive" or "mirror"
	Excludes    []string
	MaxEntries  int // default 100000
	TokenSecret []byte
	TokenTTL    time.Duration
	Now         func() time.Time // injectable for tests; nil = time.Now
}

// Result is the computed plan. It mirrors the fields of dto.BackupPlan.
type Result struct {
	Files       int
	Bytes       int64
	Estimated   bool
	Warnings    []string
	Deletions   []dto.DeletedItem
	DeleteCount int
	DeleteBytes int64
	Token       string
}

// Plan walks the local tree, optionally lists the remote subtree and returns
// the plan plus a signed token. Additive mode never calls remote; it is safe to
// pass a nil RemoteLister in that mode. A nil walk uses WalkLocal.
func Plan(ctx context.Context, req Request, walk LocalWalkFunc, remote RemoteLister) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	mode, err := normalizeMode(req.Mode)
	if err != nil {
		return Result{}, err
	}
	if walk == nil {
		walk = WalkLocal
	}

	maxEntries := req.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	patterns := normalizeExcludes(req.Excludes)

	var res Result
	local := make(map[string]struct{})
	walked := 0

	err = walk(ctx, req.LocalRoot, func(f LocalFile) error {
		walked++
		if walked%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		rel := normalizeRel(f.Path)
		if rel == "" {
			return nil
		}

		// Record presence before applying excludes. A file that is excluded
		// from upload still exists locally, so an rclone mirror using the same
		// excludes must not treat the remote copy as a deletion.
		if mode == ModeMirror {
			local[strings.ToLower(rel)] = struct{}{}
		}
		if isExcluded(rel, patterns) {
			return nil
		}

		res.Files++
		res.Bytes += f.Size
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("planner: walk source: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if mode == ModeMirror {
		if remote == nil {
			return Result{}, errors.New("planner: mirror mode requires a remote lister")
		}
		entries, err := remote.ListRecursive(ctx, req.RemotePath)
		if err != nil {
			return Result{}, fmt.Errorf("planner: list remote: %w", err)
		}
		res.Deletions, res.DeleteCount, res.DeleteBytes, res.Estimated, res.Warnings, err =
			collectDeletions(ctx, entries, local, patterns, maxEntries)
		if err != nil {
			return Result{}, err
		}
	}

	now := time.Now
	if req.Now != nil {
		now = req.Now
	}
	ttl := req.TokenTTL
	if ttl == 0 {
		ttl = DefaultTokenTTL
	}
	expiry := now().Add(ttl).Unix()
	res.Token = makeToken(req.TokenSecret, mode, req.LocalRoot, req.RemotePath,
		res.DeleteCount, res.DeleteBytes, expiry)
	return res, nil
}

// VerifyToken checks that token was produced by Plan for the same mode, roots
// and deletion totals, and that it has not expired according to req.Now.
func VerifyToken(secret []byte, token string, req Request) error {
	raw, err := hex.DecodeString(strings.TrimSpace(token))
	if err != nil || len(raw) != tokenPayloadLen+sha256.Size {
		return ErrInvalidToken
	}

	payload := raw[:tokenPayloadLen]
	sig := raw[tokenPayloadLen:]
	if payload[0] != tokenVersion {
		return ErrInvalidToken
	}
	expiry := int64(binary.BigEndian.Uint64(payload[1:9]))
	deleteCount := int64(binary.BigEndian.Uint64(payload[9:17]))
	deleteBytes := int64(binary.BigEndian.Uint64(payload[17:25]))

	mode, err := normalizeMode(req.Mode)
	if err != nil {
		return ErrInvalidToken
	}

	want := signToken(secret, mode, req.LocalRoot, req.RemotePath,
		int(deleteCount), deleteBytes, expiry)
	if !hmac.Equal(sig, want) {
		return ErrInvalidToken
	}

	now := time.Now
	if req.Now != nil {
		now = req.Now
	}
	if now().Unix() >= expiry {
		return fmt.Errorf("%w: expired at %d", ErrTokenExpired, expiry)
	}
	return nil
}

// WalkLocal is the default LocalWalkFunc. It skips symlinks and reparse
// points, Windows system directories and the two fixed-size system files that
// live at the root of every Windows volume.
func WalkLocal(ctx context.Context, root string, fn func(LocalFile) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(root) == "" {
		return errors.New("planner: source root is empty")
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("planner: source root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("planner: source root is not a directory: %s", root)
	}
	root = filepath.Clean(root)

	seen := 0
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == root {
				return walkErr
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		seen++
		if seen%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if p == root {
			return nil
		}

		name := d.Name()
		if d.IsDir() {
			if ignoredDir(name) {
				return fs.SkipDir
			}
			// WalkDir already refuses to follow symlinks, but a Windows
			// junction can look like a directory to a naive caller. Use the
			// FileInfo mode as a second guard.
			if fi, ierr := d.Info(); ierr == nil && fi.Mode()&fs.ModeSymlink != 0 {
				return fs.SkipDir
			}
			return nil
		}

		if ignoredFile(name) || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		mode := fi.Mode()
		if !mode.IsRegular() || mode&fs.ModeSymlink != 0 {
			return nil
		}

		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return fmt.Errorf("planner: relative path for %s: %w", p, rerr)
		}
		rel = filepath.ToSlash(rel)
		if rel == "" || rel == "." {
			return nil
		}
		return fn(LocalFile{Path: rel, Size: fi.Size()})
	})
}

func collectDeletions(ctx context.Context, entries []dto.Entry, local map[string]struct{}, patterns []string, maxEntries int) (
	deletions []dto.DeletedItem, count int, bytes int64, estimated bool, warnings []string, err error,
) {
	sliceCap := maxEntries
	if sliceCap > maxReportedDeletions {
		sliceCap = maxReportedDeletions
	}
	if sliceCap < 0 {
		sliceCap = 0
	}

	seen := make(map[string]struct{})
	for i, e := range entries {
		if i%512 == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, 0, 0, false, nil, cerr
			}
		}
		if e.IsDir {
			continue
		}

		rel := normalizeRel(e.Path)
		if rel == "" {
			continue
		}
		key := strings.ToLower(rel)
		if _, ok := local[key]; ok {
			continue
		}
		// rclone's sync leaves destination files matched by --exclude alone
		// (there is no --delete-excluded here), so they are not deletions. The
		// local side already skips them from the upload count for the same
		// reason; counting them here would overstate the warning.
		if isExcluded(rel, patterns) {
			continue
		}
		// Remote aliases (case or leading-slash differences) are the same
		// object; only report the first spelling.
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		count++
		bytes += e.Size
		if len(deletions) < sliceCap {
			deletions = append(deletions, dto.DeletedItem{Path: rel, Size: e.Size})
		}
		if count > maxEntries && !estimated {
			estimated = true
			warnings = append(warnings, fmt.Sprintf(
				"deletion list truncated: more than %d remote files would be deleted", maxEntries))
		}
	}

	sort.Slice(deletions, func(i, j int) bool {
		li := strings.ToLower(deletions[i].Path)
		lj := strings.ToLower(deletions[j].Path)
		if li != lj {
			return li < lj
		}
		return deletions[i].Path < deletions[j].Path
	})
	return deletions, count, bytes, estimated, warnings, nil
}

func normalizeMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case ModeAdditive, ModeMirror:
		return mode, nil
	default:
		return "", fmt.Errorf("planner: unsupported mode %q", mode)
	}
}

// normalizeRel converts a local or remote relative path into a canonical
// forward-slash form without a leading slash. It also resolves "." and
// duplicate separators so aliases compare equal.
func normalizeRel(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = strings.TrimLeft(p, "/")
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	return strings.TrimLeft(p, "/")
}

func normalizeExcludes(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
		for strings.HasPrefix(p, "./") {
			p = p[2:]
		}
		// A leading slash anchors the rule to the sync root in rclone, so it
		// has to survive normalisation: isExcluded and RcloneRules both use it.
		anchored := strings.HasPrefix(p, "/")
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		if anchored {
			p = "/" + p
		}
		out = append(out, strings.ToLower(p))
	}
	return out
}

// RcloneRules expands exclude patterns into the rules handed to rclone.
// rclone matches a bare rule against a leaf name only, while the planner and
// the wizard treat a matching directory as excluding its whole subtree, so
// each rule gets a "/**" companion. A leading "/" stays as rclone's root
// anchor.
func RcloneRules(patterns []string) []string {
	norm := normalizeExcludes(patterns)
	out := make([]string, 0, len(norm)*2)
	seen := make(map[string]struct{}, len(norm)*2)
	add := func(rule string) {
		if rule == "" {
			return
		}
		if _, ok := seen[rule]; ok {
			return
		}
		seen[rule] = struct{}{}
		out = append(out, rule)
	}
	for _, p := range norm {
		add(p)
		if !strings.HasSuffix(p, "/**") {
			add(strings.TrimSuffix(p, "/") + "/**")
		}
	}
	return out
}

// Excluded reports whether the slash-relative path rel matches any of
// patterns, using the same rules as Plan. Both arguments are normalized here so
// callers can pass raw user input and native separators.
func Excluded(rel string, patterns []string) bool {
	return isExcluded(normalizeRel(rel), normalizeExcludes(patterns))
}

// matchesRule reports whether rule matches p. An unanchored rule matches at
// any component boundary, like rclone's (^|/)rule regex: the whole path or any
// suffix that starts after a slash. An anchored rule only matches the whole
// (root-relative) path.
func matchesRule(rule, p string, anchored bool) bool {
	if matched, err := path.Match(rule, p); err == nil && matched {
		return true
	}
	if anchored {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] != '/' || i+1 >= len(p) {
			continue
		}
		if matched, err := path.Match(rule, p[i+1:]); err == nil && matched {
			return true
		}
	}
	return false
}

// isExcluded applies simple, case-insensitive globs to a relative path. An
// unanchored rule matches the full path at any component boundary and every
// ancestor directory (directory rules exclude their subtree). A leading "/"
// anchors the rule to the sync root, exactly like rclone, so it never matches
// a bare base name deeper in the tree.
func isExcluded(rel string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	lower := strings.ToLower(rel)

	var prefixes []string
	for i := 0; i < len(lower); i++ {
		if lower[i] == '/' && i > 0 {
			prefixes = append(prefixes, lower[:i])
		}
	}

	for _, pat := range patterns {
		anchored := strings.HasPrefix(pat, "/")
		rule := strings.TrimPrefix(pat, "/")

		if matchesRule(rule, lower, anchored) {
			return true
		}
		for _, prefix := range prefixes {
			if matchesRule(rule, prefix, anchored) {
				return true
			}
		}
	}
	return false
}

func ignoredDir(name string) bool {
	return strings.EqualFold(name, "$RECYCLE.BIN") ||
		strings.EqualFold(name, "System Volume Information")
}

func ignoredFile(name string) bool {
	return strings.EqualFold(name, "pagefile.sys") ||
		strings.EqualFold(name, "hiberfil.sys")
}

// ------------------------------------------------------------------- token

const (
	tokenVersion    byte = 1
	tokenPayloadLen      = 1 + 8 + 8 + 8 // version, expiry, delete count, delete bytes
)

func makeToken(secret []byte, mode, root, remotePath string, deleteCount int, deleteBytes, expiry int64) string {
	payload := make([]byte, tokenPayloadLen)
	payload[0] = tokenVersion
	binary.BigEndian.PutUint64(payload[1:9], uint64(expiry))
	binary.BigEndian.PutUint64(payload[9:17], uint64(int64(deleteCount)))
	binary.BigEndian.PutUint64(payload[17:25], uint64(deleteBytes))

	sig := signToken(secret, mode, root, remotePath, deleteCount, deleteBytes, expiry)
	out := make([]byte, 0, len(payload)+len(sig))
	out = append(out, payload...)
	out = append(out, sig...)
	return hex.EncodeToString(out)
}

func signToken(secret []byte, mode, root, remotePath string, deleteCount int, deleteBytes, expiry int64) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(canonicalString(mode, root, remotePath, deleteCount, deleteBytes, expiry)))
	return mac.Sum(nil)
}

// canonicalString is length-prefixed so path values containing separators or
// newlines cannot make two different plans sign the same message.
func canonicalString(mode, root, remotePath string, deleteCount int, deleteBytes, expiry int64) string {
	var b strings.Builder
	b.WriteString("planner/v1")
	writeField(&b, mode)
	writeField(&b, root)
	writeField(&b, remotePath)
	writeField(&b, strconv.Itoa(deleteCount))
	writeField(&b, strconv.FormatInt(deleteBytes, 10))
	writeField(&b, strconv.FormatInt(expiry, 10))
	return b.String()
}

func writeField(b *strings.Builder, s string) {
	b.WriteByte('\n')
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}
