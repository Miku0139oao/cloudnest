package services

import "testing"

func TestRemoteDir(t *testing.T) {
	cases := map[string]string{
		"a/b/c.txt": "a/b",
		"a.txt":     "",
		"/a/b.txt":  "a",
		"a//b.txt":  "a",
	}
	for in, want := range cases {
		if got := remoteDir(in); got != want {
			t.Errorf("remoteDir(%q) = %q, want %q", in, got, want)
		}
	}
	if base := remoteBase("a/b/c.txt"); base != "c.txt" {
		t.Errorf("remoteBase = %q", base)
	}
}
