package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"cloudnest/internal/rc"
)

func TestValidateOAuthClient(t *testing.T) {
	const id = "12345-abc.apps.googleusercontent.com"
	if err := validateOAuthClient(id, "GOCSPX-secret"); err != nil {
		t.Fatalf("valid pair rejected: %v", err)
	}
	cases := map[string][2]string{
		"empty id":       {"", "GOCSPX-secret"},
		"empty secret":   {id, ""},
		"wrong suffix":   {"12345-abc.apps.example.com", "GOCSPX-secret"},
		"embedded space": {id, "GOCSPX secret"},
		"newline":        {id, "GOCSPX\nsecret"},
	}
	for name, pair := range cases {
		if err := validateOAuthClient(pair[0], pair[1]); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestStoredClientParamsKeepsCustomApp proves a re-sign-in reuses the remote's
// own OAuth app: rclone authorize needs the pair explicitly, it does not read
// it from the remote section.
func TestStoredClientParamsKeepsCustomApp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/get" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"type":"drive","client_id":"12345-abc.apps.googleusercontent.com","client_secret":"GOCSPX-secret","token":"{}"}`))
	}))
	defer srv.Close()

	s := &Service{eng: &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)}, log: testLogger()}
	got, err := s.storedClientParams(rc.New(srv.URL, "", "", nil), "gdrive")
	if err != nil {
		t.Fatalf("storedClientParams: %v", err)
	}
	if got["client_id"] != "12345-abc.apps.googleusercontent.com" || got["client_secret"] != "GOCSPX-secret" {
		t.Fatalf("storedClientParams = %v, want the remote's custom pair", got)
	}
}

func TestStoredClientParamsIgnoresSharedRemote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"type":"drive"}`))
	}))
	defer srv.Close()

	s := &Service{eng: &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)}, log: testLogger()}
	got, err := s.storedClientParams(rc.New(srv.URL, "", "", nil), "gdrive")
	if err != nil {
		t.Fatalf("storedClientParams: %v", err)
	}
	if got != nil {
		t.Fatalf("storedClientParams = %v, want nil for the shared client", got)
	}
}

// TestStoredClientParamsReportsReadFailure pins that a failed config read is an
// error, not "this remote has no custom client": treating it as the latter
// would sign in with rclone's shared app and overwrite the real credentials.
func TestStoredClientParamsReportsReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rcFail(w, http.StatusInternalServerError, "engine is restarting")
	}))
	defer srv.Close()

	s := &Service{eng: &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)}, log: testLogger()}
	got, err := s.storedClientParams(rc.New(srv.URL, "", "", nil), "gdrive")
	if err == nil {
		t.Fatalf("storedClientParams = %v/nil, want an error", got)
	}
}

// TestPersistTokenWritesClientApp pins that the credentials travel with the
// token: the remote must store the client which minted it, or the next refresh
// fails. It also pins the update-first order: rclone 1.76 accepts
// config/create for an existing name and replaces the whole section, dropping
// scope, root_folder_id and team_drive.
func TestPersistTokenWritesClientApp(t *testing.T) {
	var mu sync.Mutex
	var updated map[string]any
	var creates int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config/update":
			var body struct {
				Parameters map[string]any `json:"parameters"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			updated = body.Parameters
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case "/config/create":
			atomic.AddInt32(&creates, 1)
			_, _ = w.Write([]byte(`{}`))
		case "/operations/about":
			_, _ = w.Write([]byte(`{}`))
		default:
			rcFail(w, http.StatusNotFound, "not found")
		}
	}))
	defer srv.Close()

	s := &Service{eng: &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)}, log: testLogger(), store: newServiceStore(t)}
	_ = s.persistToken(context.Background(), "drive", "gdrive", `{"access_token":"a"}`,
		map[string]string{"client_id": "12345-abc.apps.googleusercontent.com", "client_secret": "GOCSPX-secret"})

	mu.Lock()
	defer mu.Unlock()
	if got := atomic.LoadInt32(&creates); got != 0 {
		t.Fatalf("config/create calls = %d, want 0 (it would replace the section)", got)
	}
	if updated["token"] != `{"access_token":"a"}` {
		t.Fatalf("token = %v", updated["token"])
	}
	if updated["client_id"] != "12345-abc.apps.googleusercontent.com" || updated["client_secret"] != "GOCSPX-secret" {
		t.Fatalf("client app not written with the token: %v", updated)
	}
}

// TestPersistTokenCreatesMissingSection pins the other half: a remote which
// does not exist yet is created, and only a missing section falls through.
func TestPersistTokenCreatesMissingSection(t *testing.T) {
	var creates int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config/update":
			rcFail(w, http.StatusInternalServerError, "couldn't find type field in config")
		case "/config/create":
			atomic.AddInt32(&creates, 1)
			_, _ = w.Write([]byte(`{}`))
		case "/operations/about":
			_, _ = w.Write([]byte(`{}`))
		default:
			rcFail(w, http.StatusNotFound, "not found")
		}
	}))
	defer srv.Close()

	s := &Service{eng: &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)}, log: testLogger(), store: newServiceStore(t)}
	_ = s.persistToken(context.Background(), "drive", "gdrive", `{"access_token":"a"}`, nil)
	if got := atomic.LoadInt32(&creates); got != 1 {
		t.Fatalf("config/create calls = %d, want 1", got)
	}
}

// TestPersistTokenDoesNotCreateOnOtherErrors pins that a config/update failure
// which is not "section missing" never falls through to create.
func TestPersistTokenDoesNotCreateOnOtherErrors(t *testing.T) {
	var creates int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config/update":
			rcFail(w, http.StatusInternalServerError, "invalid token")
		case "/config/create":
			atomic.AddInt32(&creates, 1)
			_, _ = w.Write([]byte(`{}`))
		default:
			rcFail(w, http.StatusNotFound, "not found")
		}
	}))
	defer srv.Close()

	s := &Service{eng: &serviceTestEngine{client: rc.New(srv.URL, "", "", nil)}, log: testLogger(), store: newServiceStore(t)}
	if err := s.persistToken(context.Background(), "drive", "gdrive", `{"access_token":"a"}`, nil); err == nil {
		t.Fatal("persistToken succeeded although config/update failed")
	}
	if got := atomic.LoadInt32(&creates); got != 0 {
		t.Fatalf("config/create calls = %d, want 0", got)
	}
}
