package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateGetAndUpdateSource(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()
	created := createTestSource(t, store, "source-1", "example")

	if created.Status != SourceStatusIdle {
		t.Errorf("created status = %q, want %q", created.Status, SourceStatusIdle)
	}
	if !created.CreatedAt.Equal(created.CreatedAt.UTC()) || created.CreatedAt.IsZero() {
		t.Errorf("created_at = %s, want a non-zero UTC value", created.CreatedAt)
	}

	created.Name = "Renamed example"
	created.ConfigYAML = "site_name: Renamed example"
	created.IsActive = false
	updated, err := store.UpdateSource(context.Background(), created)
	if err != nil {
		t.Fatalf("UpdateSource(): %v", err)
	}
	if !updated.UpdatedAt.After(updated.CreatedAt) && !updated.UpdatedAt.Equal(updated.CreatedAt) {
		t.Errorf("updated_at = %s, want a time not before created_at %s", updated.UpdatedAt, updated.CreatedAt)
	}

	fetched, err := store.GetSource(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetSource(): %v", err)
	}
	if fetched.Name != "Renamed example" || fetched.ConfigYAML != "site_name: Renamed example" || fetched.IsActive {
		t.Errorf("fetched source = %#v, want updated values", fetched)
	}
}

func TestCreateSourceRejectsDuplicateSlug(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()
	createTestSource(t, store, "source-1", "same-slug")

	_, err := store.CreateSource(context.Background(), NewSource{
		ID:         "source-2",
		Slug:       "same-slug",
		Name:       "Another source",
		ConfigYAML: "site_name: Another source",
		IsActive:   true,
	})
	if err == nil {
		t.Fatal("CreateSource() with duplicate slug succeeded")
	}
}

func TestStartAndCompleteRunUpdatesSource(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")
	startedAt := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	run, err := store.StartRun(context.Background(), NewRun{
		ID:        "run-1",
		SourceID:  source.ID,
		StartedAt: startedAt,
	})
	if err != nil {
		t.Fatalf("StartRun(): %v", err)
	}
	if run.Status != RunStatusRunning || run.StartedAt.Location() != time.UTC {
		t.Errorf("started run = %#v, want running with UTC start time", run)
	}

	runningSource, err := store.GetSource(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("GetSource() while running: %v", err)
	}
	if runningSource.Status != SourceStatusRunning {
		t.Errorf("source status = %q, want %q", runningSource.Status, SourceStatusRunning)
	}

	finishedAt := time.Date(2026, time.September, 1, 10, 5, 0, 0, time.UTC)
	completed, err := store.CompleteRun(context.Background(), CompleteRun{
		ID:               run.ID,
		Status:           RunStatusSuccess,
		CompletionStatus: CompletionStatusComplete,
		FinishedAt:       finishedAt,
		AddedCount:       2,
		UpdatedCount:     1,
		SeenCount:        5,
	})
	if err != nil {
		t.Fatalf("CompleteRun(): %v", err)
	}
	if completed.Status != RunStatusSuccess || completed.AddedCount != 2 || !completed.FinishedAt.Equal(finishedAt) {
		t.Errorf("completed run = %#v, want persisted completion", completed)
	}

	completedSource, err := store.GetSource(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("GetSource() after completion: %v", err)
	}
	if completedSource.Status != SourceStatusSuccess || !completedSource.LastRunAt.Equal(finishedAt) || completedSource.LastError != "" {
		t.Errorf("completed source = %#v, want successful run state", completedSource)
	}
}

func TestCompleteRunRejectsSecondCompletion(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")
	run, err := store.StartRun(context.Background(), NewRun{ID: "run-1", SourceID: source.ID, StartedAt: time.Now()})
	if err != nil {
		t.Fatalf("StartRun(): %v", err)
	}

	completion := CompleteRun{ID: run.ID, Status: RunStatusFailed, CompletionStatus: CompletionStatusUnknown, FinishedAt: time.Now(), ErrorMessage: "site unavailable"}
	if _, err := store.CompleteRun(context.Background(), completion); err != nil {
		t.Fatalf("first CompleteRun(): %v", err)
	}
	if _, err := store.CompleteRun(context.Background(), completion); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("second CompleteRun() error = %v, want ErrInvalidTransition", err)
	}
}

func TestLastRunReturnsMostRecentRunForSource(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")

	for _, testRun := range []NewRun{
		{ID: "run-old", SourceID: source.ID, StartedAt: time.Date(2026, time.September, 1, 8, 0, 0, 0, time.UTC)},
		{ID: "run-new", SourceID: source.ID, StartedAt: time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC)},
	} {
		if _, err := store.StartRun(context.Background(), testRun); err != nil {
			t.Fatalf("StartRun(%s): %v", testRun.ID, err)
		}
	}

	last, err := store.LastRun(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("LastRun(): %v", err)
	}
	if last.ID != "run-new" {
		t.Errorf("last run ID = %q, want run-new", last.ID)
	}
}

func createTestSource(t *testing.T, store *Store, id, slug string) Source {
	t.Helper()

	source, err := store.CreateSource(context.Background(), NewSource{
		ID:         id,
		Slug:       slug,
		Name:       "Example",
		ConfigYAML: "site_name: Example",
		IsActive:   true,
	})
	if err != nil {
		t.Fatalf("CreateSource(): %v", err)
	}
	return source
}

func TestGetSourceReturnsNotFound(t *testing.T) {
	t.Parallel()

	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer store.Close()

	if _, err := store.GetSource(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSource() error = %v, want ErrNotFound", err)
	}
}
