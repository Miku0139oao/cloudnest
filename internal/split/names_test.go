package split

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeNameRejectsBadNames(t *testing.T) {
	bad := []string{
		"",
		"..",
		"..\\evil",
		"folder/../evil",
		"folder/..",
		"CON",
		"con.txt",
		"LPT9.log",
		"a<b",
		`a:b`,
		"a\x00b",
		"a\nb",
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			if got, err := SanitizeName(name); err == nil {
				t.Fatalf("SanitizeName(%q) = %q, want error", name, got)
			}
		})
	}
}

func TestSanitizeNameGoodNames(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"report.txt", "report.txt"},
		{"C:\\Users\\me\\報告.pdf", "報告.pdf"},
		{"dir/sub/file.bin", "file.bin"},
		{"name. . ", "name"},
		{"  leading space.txt", "  leading space.txt"},
		{"旅遊 報告 🗻.bin", "旅遊 報告 🗻.bin"},
	}
	for _, tc := range cases {
		got, err := SanitizeName(tc.in)
		if err != nil {
			t.Fatalf("SanitizeName(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("SanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeNameTruncatesUTF8(t *testing.T) {
	longASCII := strings.Repeat("a", 300)
	got, err := SanitizeName(longASCII)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 240 {
		t.Fatalf("truncated length = %d, want 240", len(got))
	}
	if !strings.HasSuffix(got, "~") {
		t.Fatalf("truncated name %q does not end in ~", got)
	}

	longCJK := strings.Repeat("旅", 200) + ".bin"
	got, err = SanitizeName(longCJK)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 240 {
		t.Fatalf("CJK truncated length = %d, want <= 240", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a UTF-8 sequence: %q", got)
	}
	if !strings.HasSuffix(got, "~") {
		t.Fatalf("CJK truncated name %q does not end in ~", got)
	}
}

func TestPartsDirName(t *testing.T) {
	if got := PartsDirName("movie.mkv"); got != "movie.mkv.cloudbox-part" {
		t.Fatalf("PartsDirName = %q", got)
	}
	if got := PartsDirName("CON"); got != "" {
		t.Fatalf("PartsDirName(CON) = %q, want fallback signal", got)
	}
	if got := PartsDirName("..\\evil"); got != "" {
		t.Fatalf("PartsDirName(traversal) = %q, want fallback signal", got)
	}
	if got := PartsDirName("a<b"); got != "" {
		t.Fatalf("PartsDirName(illegal) = %q, want fallback signal", got)
	}

	long := strings.Repeat("a", 300)
	got := PartsDirName(long)
	if got == "" {
		t.Fatal("PartsDirName(long) unexpectedly returned fallback")
	}
	if len(got) > 255 {
		t.Fatalf("PartsDirName(long) length = %d, want <= 255", len(got))
	}
	if !strings.HasSuffix(got, ".cloudbox-part") {
		t.Fatalf("PartsDirName(long) = %q", got)
	}
}

func TestFindConflicts(t *testing.T) {
	dir := t.TempDir()
	name := "report.txt"

	got, exists := FindConflicts(dir, name)
	if got != name || exists {
		t.Fatalf("empty dir: got (%q, %v), want (%q, false)", got, exists, name)
	}

	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, exists = FindConflicts(dir, name)
	if got != "report (1).txt" || !exists {
		t.Fatalf("one conflict: got (%q, %v)", got, exists)
	}

	if err := os.WriteFile(filepath.Join(dir, got), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, exists = FindConflicts(dir, name)
	if got != "report (2).txt" || !exists {
		t.Fatalf("two conflicts: got (%q, %v)", got, exists)
	}
}
