// Journal persistence.
//
// History used to live only in memory, so every restart started from an empty
// Activity page and an interrupted backup could not be resumed. Each job is
// written to <DataDir>/history.json together with the request that started it;
// on the next launch, entries that were still running are offered for resume.
package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"cloudnest/internal/dto"
)

const journalLimit = 50

// loadJournal restores the persisted history. Entries that were running when
// the app exited become "interrupted" so the UI can offer to resume them.
func (s *Service) loadJournal() {
	if s.journalPath == "" {
		return
	}
	b, err := os.ReadFile(s.journalPath)
	if err != nil {
		return
	}
	var j dto.Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range j.Entries {
		if e.State == "running" {
			e.State = "interrupted"
		}
		s.journalEntries = append(s.journalEntries, e)
		if rec, ok := recordFromEntry(e); ok {
			s.records = append(s.records, rec)
		}
	}
	if len(s.journalEntries) > journalLimit {
		s.journalEntries = s.journalEntries[:journalLimit]
	}
	if len(s.records) > journalLimit {
		s.records = s.records[:journalLimit]
	}
}

// journalStart records a job that just started, including its request so it
// can be resumed after a restart.
func (s *Service) journalStart(rec dto.Record, req dto.PlanRequest) {
	s.mu.Lock()
	s.journalEntries = append([]dto.JournalEntry{{
		ID: rec.ID, Kind: rec.Kind, Title: rec.Title, State: "running",
		StartedAt: rec.StartedAt, Bytes: rec.Bytes, Files: rec.Files,
		Request: req,
	}}, s.journalEntries...)
	if len(s.journalEntries) > journalLimit {
		s.journalEntries = s.journalEntries[:journalLimit]
	}
	s.mu.Unlock()
	s.persistJournal()
}

// journalFinish updates the entry with the final counters.
func (s *Service) journalFinish(rec dto.Record) {
	s.mu.Lock()
	found := false
	for i := range s.journalEntries {
		if s.journalEntries[i].ID != rec.ID {
			continue
		}
		s.journalEntries[i].State = rec.State
		// A resumed job keeps its id but starts a new attempt; without updating
		// StartedAt a reload would date the outcome from the first attempt.
		s.journalEntries[i].StartedAt = rec.StartedAt
		s.journalEntries[i].FinishedAt = rec.FinishedAt
		s.journalEntries[i].Bytes = rec.Bytes
		s.journalEntries[i].Files = rec.Files
		s.journalEntries[i].Failed = rec.Failed
		s.journalEntries[i].Error = rec.Error
		found = true
		break
	}
	s.mu.Unlock()
	if found {
		s.persistJournal()
	}
}

// PendingResume lists jobs which can still be restarted from a stored request,
// newest first.
func (s *Service) PendingResume() []dto.JournalEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]dto.JournalEntry, 0, 2)
	for _, e := range s.journalEntries {
		// Only jobs with a stored request can actually be restarted, and the
		// scheduler's synthetic "schedule-*" records carry a request but have no
		// transfer behind them, so they must never appear in the resume banner.
		if len(e.Request.SourcePaths) == 0 {
			continue
		}
		if strings.HasPrefix(e.ID, "schedule-") || strings.HasPrefix(e.ID, "restart-") {
			continue
		}
		switch e.State {
		case "interrupted":
			out = append(out, e)
		case "failed":
			// A failed run loaded from the journal has no jobState to resume by
			// id; offer its stored request instead. An in-memory job keeps the
			// ResumeJob path, which reuses its id and history record.
			if _, live := s.jobs[e.ID]; !live {
				out = append(out, e)
			}
		}
	}
	return out
}

// DismissResume drops a resumable job the user does not want to resume. It
// settles both the journal entry and the History record built from it: History
// is rendered from s.records, so only flipping the journal would leave a stale
// "interrupted"/"failed" row offering a Retry for an id with no job behind it.
func (s *Service) DismissResume(id string) dto.Empty {
	s.mu.Lock()
	for i := range s.journalEntries {
		if s.journalEntries[i].ID != id {
			continue
		}
		switch s.journalEntries[i].State {
		case "interrupted", "failed":
			s.journalEntries[i].State = "cancelled"
		}
	}
	for i := range s.records {
		if s.records[i].ID != id {
			continue
		}
		switch s.records[i].State {
		case "interrupted", "failed":
			s.records[i].State = "cancelled"
		}
	}
	s.mu.Unlock()
	s.persistJournal()
	return dto.Empty{}
}

func (s *Service) persistJournal() {
	if s.journalPath == "" {
		return
	}
	// Serialise the snapshot with the write. Taking the snapshot under s.mu
	// alone would let an older snapshot reach the rename last and undo a
	// dismissal or a finished run.
	s.journalMu.Lock()
	defer s.journalMu.Unlock()

	s.mu.Lock()
	snapshot := dto.Journal{Entries: append([]dto.JournalEntry(nil), s.journalEntries...)}
	s.mu.Unlock()

	b, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		if s.log != nil {
			s.log.Warn("journal: marshal failed", "err", err)
		}
		return
	}
	tmp := s.journalPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		if s.log != nil {
			s.log.Warn("journal: write failed", "err", err)
		}
		return
	}
	if err := os.Rename(tmp, s.journalPath); err != nil && s.log != nil {
		s.log.Warn("journal: rename failed", "err", err)
	}
}

// recordFromEntry converts a journal entry for the Activity list.
func recordFromEntry(e dto.JournalEntry) (dto.Record, bool) {
	switch e.State {
	case "done", "failed", "cancelled", "interrupted":
	default:
		return dto.Record{}, false
	}
	state := e.State
	return dto.Record{
		ID: e.ID, Kind: e.Kind, State: state, Title: e.Title,
		StartedAt: e.StartedAt, FinishedAt: e.FinishedAt,
		Bytes: e.Bytes, Files: e.Files, Failed: e.Failed, Error: e.Error,
	}, true
}

func journalPathFor(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "history.json")
}
