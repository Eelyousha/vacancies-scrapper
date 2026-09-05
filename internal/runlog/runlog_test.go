package runlog

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"vacancies-scrapper/internal/scraper"
	"vacancies-scrapper/internal/storage"
)

func TestFinishPersistsSuccessfulResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	run := startTestRun(t, store)

	completed, err := New(store).Finish(ctx, run.ID, scraper.Result{
		SiteName: "Example", URL: "https://example.test/jobs", CardCount: 1,
		FinishedAt: time.Now(),
		Vacancies:  []scraper.Vacancy{{Title: "Go developer", Link: "https://example.test/jobs/1"}},
	}, nil, storage.CompletionStatusComplete)
	if err != nil {
		t.Fatalf("Finish(): %v", err)
	}
	if completed.Status != storage.RunStatusSuccess || completed.AddedCount != 1 || completed.SeenCount != 1 {
		t.Errorf("completed = %#v, want successful run with one vacancy", completed)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(completed.MetaJSON), &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if metadata["url"] != "https://example.test/jobs" {
		t.Errorf("metadata = %#v, want URL diagnostic", metadata)
	}
}

func TestFinishMarksDetailErrorsPartial(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	run := startTestRun(t, store)

	completed, err := New(store).Finish(ctx, run.ID, scraper.Result{
		FinishedAt:   time.Now(),
		DetailErrors: []scraper.DetailError{{Link: "https://example.test/jobs/1", Error: "timeout"}},
		Vacancies:    []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}},
	}, nil, storage.CompletionStatusComplete)
	if err != nil {
		t.Fatalf("Finish(): %v", err)
	}
	if completed.Status != storage.RunStatusPartial || completed.CompletionStatus != storage.CompletionStatusIncomplete {
		t.Errorf("completed = %#v, want partial/incomplete", completed)
	}
}

func TestFailCompletesRunningRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	run := startTestRun(t, store)

	completed, err := New(store).Fail(ctx, run.ID, "browser unavailable")
	if err != nil {
		t.Fatalf("Fail(): %v", err)
	}
	if completed.Status != storage.RunStatusFailed || completed.CompletionStatus != storage.CompletionStatusUnknown || completed.ErrorMessage != "browser unavailable" {
		t.Errorf("completed = %#v, want failed run", completed)
	}
}

func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatalf("storage.Open(): %v", err)
	}
	return store
}

func startTestRun(t *testing.T, store *storage.Store) storage.Run {
	t.Helper()
	source, err := store.CreateSource(context.Background(), storage.NewSource{
		ID: "source-1", Slug: "example", Name: "Example", ConfigYAML: "site_name: Example", IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateSource(): %v", err)
	}
	run, err := store.StartRun(context.Background(), storage.NewRun{ID: "run-1", SourceID: source.ID, StartedAt: time.Now()})
	if err != nil {
		t.Fatalf("StartRun(): %v", err)
	}
	return run
}
