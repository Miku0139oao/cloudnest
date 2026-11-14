package services

import (
	"regexp"
	"testing"
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
