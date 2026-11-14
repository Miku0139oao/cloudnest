package split

import (
	"testing"
)

func TestDefaultOptions(t *testing.T) {
	o := DefaultOptions()
	if !o.Enabled {
		t.Fatal("split should be enabled by default")
	}
	if o.Threshold != 256<<20 || o.PartSize != 256<<20 {
		t.Fatalf("unexpected byte defaults: %+v", o)
	}
	if o.MinParts != 2 || o.MaxParts != 16 {
		t.Fatalf("unexpected part defaults: %+v", o)
	}
}

func TestShouldSplitBoundaries(t *testing.T) {
	const mib = int64(1 << 20)
	o := DefaultOptions()
	cases := []struct {
		name string
		size int64
		want bool
	}{
		{"zero", 0, false},
		{"tiny", 5, false},
		{"255MiB", 255 * mib, false},
		{"exact threshold", 256 * mib, true},
		{"257MiB", 257 * mib, true},
		{"4GiB", 4 * 1024 * mib, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldSplit(tc.size, o); got != tc.want {
				t.Fatalf("ShouldSplit(%d) = %v, want %v", tc.size, got, tc.want)
			}
		})
	}

	o.Enabled = false
	if ShouldSplit(4<<30, o) {
		t.Fatal("disabled option must not split")
	}
	o = DefaultOptions()
	o.Threshold = 1
	if !ShouldSplit(1, o) {
		t.Fatal("size equal to threshold should split")
	}
}

func TestPartCountBoundaries(t *testing.T) {
	const mib = int64(1 << 20)
	o := DefaultOptions()
	fourPointTwoGiB := int64(4509715660)
	cases := []struct {
		name string
		size int64
		want int
	}{
		{"zero", 0, 0},
		{"tiny", 5, 2},
		{"255MiB", 255 * mib, 2},
		{"256MiB", 256 * mib, 2},
		{"257MiB", 257 * mib, 2},
		{"4GiB", 4 * 1024 * mib, 16},
		{"4.2GiB", fourPointTwoGiB, 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PartCount(tc.size, o); got != tc.want {
				t.Fatalf("PartCount(%d) = %d, want %d", tc.size, got, tc.want)
			}
		})
	}

	custom := Options{PartSize: mib, MinParts: 4, MaxParts: 8}
	if got := PartCount(mib, custom); got != 4 {
		t.Fatalf("min clamp = %d, want 4", got)
	}
	if got := PartCount(20*mib, custom); got != 8 {
		t.Fatalf("max clamp = %d, want 8", got)
	}
}

func TestChunkSizeAlignment(t *testing.T) {
	const mib = int64(1 << 20)
	fourPointTwoGiB := int64(4509715660)
	cases := []struct {
		name  string
		size  int64
		parts int
		want  int64
	}{
		{"255MiB two parts", 255 * mib, 2, 128 * mib},
		{"256MiB two parts", 256 * mib, 2, 128 * mib},
		{"257MiB two parts", 257 * mib, 2, 129 * mib},
		{"512MiB two parts", 512 * mib, 2, 256 * mib},
		{"513MiB three parts", 513 * mib, 3, 171 * mib},
		{"4.2GiB sixteen parts", fourPointTwoGiB, 16, 269 * mib},
		{"tiny file", 5, 2, 1 * mib},
		{"zero size", 0, 2, 0},
		{"zero parts", 10, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChunkSize(tc.size, tc.parts)
			if got != tc.want {
				t.Fatalf("ChunkSize(%d, %d) = %d, want %d", tc.size, tc.parts, got, tc.want)
			}
			if got != 0 && got%(1<<20) != 0 {
				t.Fatalf("chunk size %d is not 1 MiB aligned", got)
			}
			if got != 0 && got*int64(tc.parts) < tc.size {
				// 4.2GiB fits comfortably in int64; this is only a
				// diagnostic for the test cases above.
				t.Fatalf("chunk size %d * %d does not cover %d", got, tc.parts, tc.size)
			}
		})
	}
}
