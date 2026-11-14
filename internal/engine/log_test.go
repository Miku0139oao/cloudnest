package engine

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		mustNot  []string
		mustHave []string
	}{
		{
			name:     "query secrets",
			input:    `2026/11/14 callback access_token=ya29.secret&refresh_token=1//refresh&state=xyz`,
			mustNot:  []string{"ya29.secret", "1//refresh", "xyz"},
			mustHave: []string{"access_token=[REDACTED]", "refresh_token=[REDACTED]", "state=[REDACTED]"},
		},
		{
			name:     "authorization header",
			input:    "Authorization: Basic dXNlcjpwYXNz",
			mustNot:  []string{"dXNlcjpwYXNz"},
			mustHave: []string{"Authorization: Basic [REDACTED]"},
		},
		{
			name:     "oauth url",
			input:    `open https://accounts.google.com/o/oauth2/auth?client_id=abc&state=secret&code=12345 now`,
			mustNot:  []string{"accounts.google.com", "secret", "12345"},
			mustHave: []string{"[REDACTED-URL]"},
		},
		{
			name:     "json secrets",
			input:    `{"client_secret":"sekret","authUrl":"https://oauth2.example/authorize","state":"st"}`,
			mustNot:  []string{"sekret", "oauth2.example", `"st"`},
			mustHave: []string{"[REDACTED]"},
		},
		{
			name:     "rc environment password",
			input:    `RCLONE_RC_PASS=s3cr3t-value`,
			mustNot:  []string{"s3cr3t-value"},
			mustHave: []string{"RCLONE_RC_PASS=[REDACTED]"},
		},
		{
			name:     "spaced assignments",
			input:    "client_secret = very-secret\npassword: hunter2",
			mustNot:  []string{"very-secret", "hunter2"},
			mustHave: []string{"client_secret=[REDACTED]", "password=[REDACTED]"},
		},
		{
			name:     "plain line",
			input:    "2026/11/14 NOTICE: rclone v1.70.3 is ready",
			mustHave: []string{"rclone v1.70.3 is ready"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.input)
			for _, secret := range tc.mustNot {
				if strings.Contains(got, secret) {
					t.Errorf("Redact(%q) = %q; still contains %q", tc.input, got, secret)
				}
			}
			for _, want := range tc.mustHave {
				if !strings.Contains(got, want) {
					t.Errorf("Redact(%q) = %q; missing %q", tc.input, got, want)
				}
			}
		})
	}
}

func TestRingBufferKeepsLastLines(t *testing.T) {
	ring := NewRingBuffer(3)
	if _, err := ring.Write([]byte("1\n2\n3\n4\n")); err != nil {
		t.Fatal(err)
	}
	lines := ring.Lines()
	if len(lines) != 3 || lines[0] != "2" || lines[2] != "4" {
		t.Fatalf("lines = %v", lines)
	}
	if _, err := ring.Write([]byte("5")); err != nil {
		t.Fatal(err)
	}
	if ring.Len() != 3 || ring.Lines()[2] != "4" {
		t.Fatalf("partial line was emitted early: %v", ring.Lines())
	}
	ring.Flush()
	if lines = ring.Lines(); lines[2] != "5" {
		t.Fatalf("lines after flush = %v", lines)
	}
	ring.Append("6")
	if ring.Lines()[2] != "6" {
		t.Fatalf("lines after append = %v", ring.Lines())
	}
	if !strings.Contains(ring.String(), "4\n5\n6") {
		t.Fatalf("String = %q", ring.String())
	}
}

func TestRingBufferDefaultSize(t *testing.T) {
	ring := NewRingBuffer(0)
	for i := 0; i < 250; i++ {
		ring.Append("x")
	}
	if ring.Len() != 200 {
		t.Fatalf("Len = %d, want 200", ring.Len())
	}
}

func TestLineWriter(t *testing.T) {
	var lines []string
	w := newLineWriter(func(line string) { lines = append(lines, line) })
	if _, err := w.Write([]byte("a\nb")); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "a" {
		t.Fatalf("lines = %v", lines)
	}
	if _, err := w.Write([]byte("c\r\n")); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[1] != "bc" {
		t.Fatalf("lines = %v", lines)
	}
	w.Flush()
	if len(lines) != 2 {
		t.Fatalf("flush duplicated a complete line: %v", lines)
	}
	if _, err := w.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if len(lines) != 3 || lines[2] != "tail" {
		t.Fatalf("lines = %v", lines)
	}
}
