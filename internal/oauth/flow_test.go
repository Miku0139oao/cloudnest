package oauth

import (
	"strings"
	"testing"
	"time"
)

// TestAuthorizeClientArgs pins rclone's positional form: `authorize <provider>
// <client_id> <client_secret>`. The flag-style `client_id=x` form is parsed as
// the literal client id, which sends a broken value to Google.
func TestAuthorizeClientArgs(t *testing.T) {
	id, secret, err := authorizeClientArgs(map[string]string{
		"client_id":     " 12345-abc.apps.googleusercontent.com ",
		"client_secret": " GOCSPX-secret ",
	})
	if err != nil {
		t.Fatalf("authorizeClientArgs: %v", err)
	}
	if id != "12345-abc.apps.googleusercontent.com" || secret != "GOCSPX-secret" {
		t.Fatalf("args = %q/%q, want trimmed pair", id, secret)
	}

	if id, secret, err := authorizeClientArgs(nil); err != nil || id != "" || secret != "" {
		t.Fatalf("nil params = %q/%q/%v, want empty pair and no error", id, secret, err)
	}
}

func TestAuthorizeClientArgsRejectsUnsafeValues(t *testing.T) {
	cases := map[string]map[string]string{
		"only id":        {"client_id": "12345-abc.apps.googleusercontent.com"},
		"only secret":    {"client_secret": "GOCSPX-secret"},
		"flag lookalike": {"client_id": "--config=/tmp/evil", "client_secret": "x"},
		"space":          {"client_id": "12345-abc.apps.googleusercontent.com", "client_secret": "a b"},
		"too long":       {"client_id": "12345-abc.apps.googleusercontent.com", "client_secret": strings.Repeat("x", 513)},
	}
	for name, params := range cases {
		if _, _, err := authorizeClientArgs(params); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestBeginDoesNotDeadlockWithLogger pins the deadlock which made every
// sign-in hang at "preparing": Begin holds the manager lock while it logs, and
// the logger used to take the same lock. The rclone path does not exist, so a
// non-deadlocking Begin returns an error immediately.
func TestBeginDoesNotDeadlockWithLogger(t *testing.T) {
	m := New(`C:\does-not-exist\rclone.exe`, nil)
	m.SetLogf(func(string, ...any) {})

	done := make(chan error, 1)
	go func() {
		_, err := m.Begin("drive", "gdrive", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Begin succeeded without an rclone binary")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Begin deadlocked while logging")
	}
}

func TestRetryUnknownSession(t *testing.T) {
	m := New("rclone", nil)
	if _, err := m.Retry("missing"); err == nil {
		t.Fatal("Retry accepted an unknown session")
	}
}
