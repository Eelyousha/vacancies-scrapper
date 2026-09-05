package runner

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/runlog"
	"vacancies-scrapper/internal/scraper"
	"vacancies-scrapper/internal/storage"
)

func TestSuccessfulRunPersistsSourceVacanciesAndDiagnostics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	service := New(store)
	input := sourceInput()

	started, err := service.Start(ctx, input, "site_name: Example jobs\n")
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	result := scraper.Result{
		SiteName:   "Example jobs",
		URL:        "https://example.test/jobs",
		StartedAt:  time.Now().Add(-time.Minute),
		FinishedAt: time.Now(),
		CardCount:  1,
		Vacancies:  []scraper.Vacancy{{Title: "Go developer", Company: "Example", Salary: "100", Link: "https://example.test/jobs/1", Description: "Backend"}},
	}
	completed, err := runlog.New(store).Finish(ctx, started.Run.ID, result, nil, storage.CompletionStatusComplete)
	if err != nil {
		t.Fatalf("Finish(): %v", err)
	}
	if completed.Status != storage.RunStatusSuccess || completed.CompletionStatus != storage.CompletionStatusComplete || completed.AddedCount != 1 || completed.SeenCount != 1 {
		t.Errorf("completed run = %#v, want successful run with one added vacancy", completed)
	}

	storedSource, err := store.GetSource(ctx, started.Source.ID)
	if err != nil {
		t.Fatalf("GetSource(): %v", err)
	}
	if storedSource.ConfigYAML != "site_name: Example jobs\n" || storedSource.Status != storage.SourceStatusSuccess {
		t.Errorf("stored source = %#v, want YAML and success state", storedSource)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(completed.MetaJSON), &metadata); err != nil {
		t.Fatalf("unmarshal run metadata: %v", err)
	}
	if metadata["url"] != result.URL || metadata["card_count"] != float64(1) {
		t.Errorf("metadata = %#v, want result diagnostics", metadata)
	}
	var vacancyCount int
	if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM vacancies WHERE source_id = ?", started.Source.ID).Scan(&vacancyCount); err != nil {
		t.Fatalf("count vacancies: %v", err)
	}
	if vacancyCount != 1 {
		t.Errorf("vacancy count = %d, want 1", vacancyCount)
	}
}

func TestStartUpdatesExistingSourceForSameSiteName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	service := New(store)
	input := sourceInput()

	first, err := service.Start(ctx, input, "site_name: Example jobs\nversion: one\n")
	if err != nil {
		t.Fatalf("first Start(): %v", err)
	}
	if _, err := runlog.New(store).Fail(ctx, first.Run.ID, "interrupted"); err != nil {
		t.Fatalf("Fail(first): %v", err)
	}
	second, err := service.Start(ctx, input, "site_name: Example jobs\nversion: two\n")
	if err != nil {
		t.Fatalf("second Start(): %v", err)
	}
	if second.Source.ID != first.Source.ID {
		t.Errorf("second source ID = %q, want existing ID %q", second.Source.ID, first.Source.ID)
	}
	if second.Source.ConfigYAML != "site_name: Example jobs\nversion: two\n" {
		t.Errorf("updated YAML = %q, want latest YAML", second.Source.ConfigYAML)
	}
}

func TestFinishMarksDetailErrorsPartial(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	service := New(store)
	started, err := service.Start(ctx, sourceInput(), "site_name: Example jobs\n")
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	result := scraper.Result{
		FinishedAt:   time.Now(),
		DetailErrors: []scraper.DetailError{{Link: "https://example.test/jobs/1", Error: "timeout"}},
		Vacancies:    []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}},
	}
	completed, err := runlog.New(store).Finish(ctx, started.Run.ID, result, nil, storage.CompletionStatusComplete)
	if err != nil {
		t.Fatalf("Finish(): %v", err)
	}
	if completed.Status != storage.RunStatusPartial || completed.CompletionStatus != storage.CompletionStatusIncomplete {
		t.Errorf("completed run = %#v, want partial/incomplete", completed)
	}
}

func TestFailCompletesStartedRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	started, err := New(store).Start(ctx, sourceInput(), "site_name: Example jobs\n")
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	completed, err := runlog.New(store).Fail(ctx, started.Run.ID, "browser unavailable")
	if err != nil {
		t.Fatalf("Fail(): %v", err)
	}
	if completed.Status != storage.RunStatusFailed || completed.CompletionStatus != storage.CompletionStatusUnknown || completed.ErrorMessage != "browser unavailable" {
		t.Errorf("completed run = %#v, want failed run with error", completed)
	}
}

func sourceInput() config.Source {
	return config.Source{SiteName: "Example jobs", BaseURL: "https://example.test/jobs"}
}

func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatalf("storage.Open(): %v", err)
	}
	return store
}
