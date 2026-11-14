package services

import (
	"path/filepath"
	"testing"

	"cloudnest/internal/dto"
)

func newJournalService(t *testing.T, dir string) *Service {
	t.Helper()
	return &Service{
		jobs:        make(map[string]*jobState),
		usage:       make(map[string]usageEntry),
		journalPath: filepath.Join(dir, "history.json"),
	}
}

func TestJournalSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	first := newJournalService(t, dir)
	req := dto.PlanRequest{SourcePaths: []string{`C:\docs`}, AccountID: "gdrive", DestRoot: "電腦備份"}
	first.journalStart(dto.Record{ID: "job-1", Kind: "backup", State: "running", Title: "docs"}, req)
	first.journalFinish(dto.Record{
		ID: "job-1", Kind: "backup", State: "done", Title: "docs",
		StartedAt: 100, FinishedAt: 200, Bytes: 42, Files: 3,
	})

	second := newJournalService(t, dir)
	second.loadJournal()
	hist := second.History(10).Items
	if len(hist) != 1 {
		t.Fatalf("history = %d entries, want 1", len(hist))
	}
	if hist[0].State != "done" || hist[0].Bytes != 42 || hist[0].Files != 3 {
		t.Fatalf("restored record = %#v", hist[0])
	}
	if got := len(second.PendingResume()); got != 0 {
		t.Fatalf("finished job offered for resume: %d", got)
	}
}

func TestJournalOffersInterruptedBackup(t *testing.T) {
	dir := t.TempDir()
	first := newJournalService(t, dir)
	req := dto.PlanRequest{SourcePaths: []string{`C:\docs`}, AccountID: "gdrive", DestRoot: "電腦備份"}
	first.journalStart(dto.Record{ID: "job-2", Kind: "backup", State: "running", Title: "docs"}, req)
	// No journalFinish: the app "crashed" mid-backup.

	second := newJournalService(t, dir)
	second.loadJournal()
	pending := second.PendingResume()
	if len(pending) != 1 || pending[0].Request.AccountID != "gdrive" || len(pending[0].Request.SourcePaths) != 1 {
		t.Fatalf("pending = %#v", pending)
	}
	// The interrupted job still shows up in history, marked as failed.
	hist := second.History(10).Items
	if len(hist) != 1 || hist[0].State != "failed" {
		t.Fatalf("history = %#v", hist)
	}

	second.DismissResume("job-2")
	if got := len(second.PendingResume()); got != 0 {
		t.Fatalf("dismiss left %d pending", got)
	}
}
