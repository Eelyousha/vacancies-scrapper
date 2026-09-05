package main

import (
	"context"
	"path/filepath"
	"testing"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/runlog"
	"vacancies-scrapper/internal/runner"
	"vacancies-scrapper/internal/storage"
)

func TestRecordFailureUsesContextIndependentFromScrape(t *testing.T) {
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatalf("storage.Open(): %v", err)
	}
	defer store.Close()
	lifecycle := runner.New(store)
	started, err := lifecycle.Start(context.Background(), config.Source{SiteName: "Example", BaseURL: "https://example.test"}, "site_name: Example\n")
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := recordFailure(runlog.New(store), started.Run.ID, context.DeadlineExceeded); err != nil {
		t.Fatalf("recordFailure(): %v", err)
	}
	completed, err := store.LastRun(context.Background(), started.Source.ID)
	if err != nil {
		t.Fatalf("LastRun(): %v", err)
	}
	if completed.Status != storage.RunStatusFailed || completed.ErrorMessage != context.DeadlineExceeded.Error() {
		t.Errorf("completed run = %#v, want recorded deadline failure", completed)
	}
}
