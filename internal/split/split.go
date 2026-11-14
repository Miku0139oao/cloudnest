// Package split implements the local, transport-independent half of
// Cloudnest's large-file parallel upload feature.
//
// The remote layout is:
//
//	<name>.cloudbox-part/
//	    manifest.json
//	    part-0001.bin
//	    ...
//	    README.txt
//
// The caller is responsible for uploading/downloading the part files with
// rclone. In particular, manifest.json must be transferred last; its presence
// is the completion marker.
package split

import "math"

// Format is the manifest format identifier understood by this package.
const Format = "cloudbox-split/1"

const (
	defaultThreshold int64 = 256 << 20 // 256 MiB
	defaultPartSize  int64 = 256 << 20 // 256 MiB
	defaultMinParts        = 2
	defaultMaxParts        = 16

	maxParts = 1000
)

// PartInfo describes one part file.
type PartInfo struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest describes a split file. ModTime and CreatedAt are Unix seconds.
type Manifest struct {
	Format    string     `json:"format"`
	Name      string     `json:"name"`
	Size      int64      `json:"size"`
	ModTime   int64      `json:"modTime"`
	SHA256    string     `json:"sha256"`
	ChunkSize int64      `json:"chunkSize"`
	PartCount int        `json:"partCount"`
	Parts     []PartInfo `json:"parts"`
	CreatedBy string     `json:"createdBy,omitempty"`
	CreatedAt int64      `json:"createdAt,omitempty"`
	SourceRel string     `json:"sourceRel,omitempty"`
}

// Options controls when a file is split and how many parts are used.
type Options struct {
	Enabled   bool
	Threshold int64
	PartSize  int64
	MinParts  int
	MaxParts  int
}

// SetState is the local state of a split set directory.
type SetState string

const (
	SetAbsent     SetState = "absent"
	SetIncomplete SetState = "incomplete"
	SetComplete   SetState = "complete"
	SetCorrupt    SetState = "corrupt"
	SetForeign    SetState = "foreign"
)

// Phase is a coarse transfer phase suitable for a progress UI.
type Phase string

const (
	PhasePlanning    Phase = "planning"
	PhaseHashing     Phase = "hashing"
	PhaseStaging     Phase = "staging"
	PhaseUploading   Phase = "uploading"
	PhaseFinalizing  Phase = "finalizing"
	PhaseDownloading Phase = "downloading"
	PhaseVerifying   Phase = "verifying"
	PhaseMerging     Phase = "merging"
	PhaseDone        Phase = "done"
)

// Progress is a transport/UI-neutral progress snapshot.
type Progress struct {
	Phase      Phase
	Bytes      int64
	Total      int64
	PartsDone  int
	PartsTotal int
	Speed      float64
	ETA        *int64
	Message    string
}

// SetInfo is the result of inspecting a local split set directory.
type SetInfo struct {
	State      SetState
	Manifest   *Manifest
	PartsFound int
	Missing    []int
	Foreign    bool
}

// DefaultOptions returns the product defaults for split upload.
func DefaultOptions() Options {
	return Options{
		Enabled:   true,
		Threshold: defaultThreshold,
		PartSize:  defaultPartSize,
		MinParts:  defaultMinParts,
		MaxParts:  defaultMaxParts,
	}
}

// ShouldSplit reports whether size is large enough for split upload.
func ShouldSplit(size int64, o Options) bool {
	return o.Enabled && size > 0 && size >= o.Threshold
}

// PartCount returns clamp(ceil(size/PartSize), MinParts, MaxParts).
func PartCount(size int64, o Options) int {
	if size <= 0 {
		return 0
	}

	partSize := o.PartSize
	if partSize <= 0 {
		partSize = defaultPartSize
	}

	n := size / partSize
	if size%partSize != 0 {
		n++
	}

	minParts := o.MinParts
	if minParts < 1 {
		minParts = defaultMinParts
	}
	maxParts := o.MaxParts
	if maxParts < minParts {
		maxParts = minParts
	}

	if n < int64(minParts) {
		return minParts
	}
	if n > int64(maxParts) {
		return maxParts
	}
	return int(n)
}

// ChunkSize returns ceil(size/parts) rounded up to a 1 MiB boundary. For
// invalid arguments it returns 0.
func ChunkSize(size int64, parts int) int64 {
	if size <= 0 || parts <= 0 {
		return 0
	}
	const align int64 = 1 << 20

	chunk := size / int64(parts)
	if size%int64(parts) != 0 {
		chunk++
	}
	if chunk > math.MaxInt64-(align-1) {
		return math.MaxInt64
	}
	return (chunk + align - 1) / align * align
}

// PartsDirName returns the conventional split directory name for name.
// It returns an empty string when no safe, valid Windows directory name can be
// formed; callers should use their normal fallback naming in that case.
func PartsDirName(name string) string {
	base, err := SanitizeName(name)
	if err != nil {
		return ""
	}
	out := base + ".cloudbox-part"
	// Keep some headroom below the common 255-character component limit.
	if len(out) > 255 || hasWindowsIllegal(out) || isReservedDeviceName(out) {
		return ""
	}
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}
