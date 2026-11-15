package services

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNormalizeDriveLetter(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"X", "X:", true},
		{"X:", "X:", true},
		{"x", "X:", true},
		{"x:", "X:", true},
		{" z: ", "Z:", true},
		{"D", "D:", true},
		{"", "", false},
		{":", "", false},
		{"::", "", false},
		{"X::", "", false},
		{"XY", "", false},
		{"1", "", false},
		{"X:\\", "", false},
		{"-", "", false},
	}
	for _, tc := range tests {
		got, ok := normalizeDriveLetter(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("normalizeDriveLetter(%q) = (%q, %v), want (%q, %v)",
				tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFreeDriveLetters(t *testing.T) {
	letters := (&Service{}).FreeDriveLetters()
	valid := regexp.MustCompile(`^[D-Z]:$`)
	seen := make(map[string]bool, len(letters))
	for _, letter := range letters {
		if !valid.MatchString(letter) {
			t.Fatalf("FreeDriveLetters returned %q, want D: through Z: only", letter)
		}
		if seen[letter] {
			t.Fatalf("FreeDriveLetters returned %q twice", letter)
		}
		seen[letter] = true
	}
	for _, reserved := range []string{"A:", "B:"} {
		if seen[reserved] {
			t.Fatalf("FreeDriveLetters must skip floppy letter %s", reserved)
		}
	}
}

func TestWaitForMountAppearsAfterRetry(t *testing.T) {
	calls := 0
	err := waitForMount(context.Background(), "X:", time.Second, time.Millisecond,
		func(context.Context) (bool, error) {
			calls++
			return calls >= 3, nil
		})
	if err != nil {
		t.Fatalf("waitForMount returned %v, want nil once the mount appears", err)
	}
	if calls != 3 {
		t.Fatalf("visible called %d times, want 3", calls)
	}
}

func TestWaitForMountTimesOut(t *testing.T) {
	err := waitForMount(context.Background(), "X:", 40*time.Millisecond, 5*time.Millisecond,
		func(context.Context) (bool, error) {
			return false, nil
		})
	if err == nil {
		t.Fatal("waitForMount returned nil, want a timeout error")
	}
	if !strings.Contains(err.Error(), "X:") || !strings.Contains(err.Error(), "did not appear") {
		t.Fatalf("waitForMount error = %q, want it to name the mount point and the failure", err)
	}
}

func TestWaitForMountKeepsListError(t *testing.T) {
	boom := errors.New("rclone went away")
	err := waitForMount(context.Background(), "X:", 40*time.Millisecond, 5*time.Millisecond,
		func(context.Context) (bool, error) {
			return false, boom
		})
	if !errors.Is(err, boom) {
		t.Fatalf("waitForMount error = %v, want it to wrap the listing error", err)
	}
}

func TestSameMountPoint(t *testing.T) {
	tests := []struct {
		listed, want string
		ok           bool
	}{
		{"X:", "X:", true},
		{"x", "X:", true},
		{`X:\`, "X:", true},
		{"X: ", "x:", true},
		{"X:", "Y:", false},
		{"/Volumes/Cloud", "/Volumes/Cloud", true},
		{"/Volumes/Cloud", "/Volumes/Other", false},
	}
	for _, tc := range tests {
		if got := sameMountPoint(tc.listed, tc.want); got != tc.ok {
			t.Errorf("sameMountPoint(%q, %q) = %v, want %v", tc.listed, tc.want, got, tc.ok)
		}
	}
}
