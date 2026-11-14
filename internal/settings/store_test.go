package settings

import (
	"testing"

	"cloudnest/internal/dto"
)

func TestScheduleSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := store.Update(func(cur *dto.Settings) {
		cur.Schedule = dto.Schedule{
			Enabled: true, Time: "03:00", AccountID: "gdrive",
			SourcePaths: []string{`C:\docs`}, DestRoot: "電腦備份", Mode: "additive",
		}
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	reloaded, err := New(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get().Schedule
	if !got.Enabled || got.Time != "03:00" || got.AccountID != "gdrive" ||
		got.DestRoot != "電腦備份" || len(got.SourcePaths) != 1 {
		t.Fatalf("schedule = %#v", got)
	}
}
