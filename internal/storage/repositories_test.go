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

func TestStartExclusiveRunRejectsRunningSource(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")
	if _, err := store.StartExclusiveRun(context.Background(), NewRun{ID: "run-1", SourceID: source.ID, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartExclusiveRun(context.Background(), NewRun{ID: "run-2", SourceID: source.ID, StartedAt: time.Now()}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("second StartExclusiveRun() error = %v, want ErrInvalidTransition", err)
	}
}

func TestUpdateSourceValidatesSchedule(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")
	source.ScheduleType = "interval"
	source.ScheduleValue = "nonsense"
	if _, err := store.UpdateSource(context.Background(), source); err == nil {
		t.Fatal("invalid interval schedule was accepted")
	}
	source.ScheduleValue = "2h"
	updated, err := store.UpdateSource(context.Background(), source)
	if err != nil || updated.ScheduleValue != "2h" {
		t.Fatalf("valid interval update = %#v, %v", updated, err)
	}
}

func TestCompleteRunArchivesOnlyVacanciesMissingFromCompleteRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")

	first := startTestRun(t, store, "run-1", source.ID)
	observed := []ObservedVacancy{
		{Title: "Seen again", Link: "https://example.test/jobs/1"},
		{Title: "Missing", Link: "https://example.test/jobs/2"},
	}
	if _, err := store.ApplyObservedVacancies(ctx, first.ID, observed); err != nil {
		t.Fatalf("apply first observations: %v", err)
	}
	if _, err := store.CompleteRun(ctx, CompleteRun{ID: first.ID, Status: RunStatusSuccess, CompletionStatus: CompletionStatusComplete, FinishedAt: time.Now()}); err != nil {
		t.Fatalf("complete first run: %v", err)
	}

	second := startTestRun(t, store, "run-2", source.ID)
	if _, err := store.ApplyObservedVacancies(ctx, second.ID, observed[:1]); err != nil {
		t.Fatalf("apply second observations: %v", err)
	}
	completed, err := store.CompleteRun(ctx, CompleteRun{ID: second.ID, Status: RunStatusSuccess, CompletionStatus: CompletionStatusComplete, FinishedAt: time.Now()})
	if err != nil {
		t.Fatalf("complete second run: %v", err)
	}
	if completed.ArchivedCount != 1 {
		t.Errorf("archived count = %d, want 1", completed.ArchivedCount)
	}

	var listingStatus, archivedAt, disposition string
	if err := store.DB.QueryRowContext(ctx, `
		SELECT v.listing_status, v.archived_at, srv.disposition
		FROM vacancies v JOIN scraping_run_vacancies srv ON srv.vacancy_id = v.id
		WHERE v.source_id = ? AND v.canonical_link = ? AND srv.run_id = ?
	`, source.ID, "https://example.test/jobs/2", second.ID).Scan(&listingStatus, &archivedAt, &disposition); err != nil {
		t.Fatalf("read archived vacancy: %v", err)
	}
	if listingStatus != "archived" || archivedAt == "" || disposition != "archived" {
		t.Errorf("archived vacancy = status %q, archived_at %q, disposition %q", listingStatus, archivedAt, disposition)
	}
}

func TestCompleteRunDoesNotArchiveAfterPartialOrFailedRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, terminal := range []struct {
		name       string
		status     RunStatus
		completion CompletionStatus
	}{
		{name: "partial", status: RunStatusPartial, completion: CompletionStatusIncomplete},
		{name: "failed", status: RunStatusFailed, completion: CompletionStatusUnknown},
	} {
		t.Run(terminal.name, func(t *testing.T) {
			store := openTestStore(t)
			defer store.Close()
			source := createTestSource(t, store, "source-1", "example")
			first := startTestRun(t, store, "run-1", source.ID)
			observed := []ObservedVacancy{{Title: "Present", Link: "https://example.test/jobs/1"}, {Title: "Must stay active", Link: "https://example.test/jobs/2"}}
			if _, err := store.ApplyObservedVacancies(ctx, first.ID, observed); err != nil {
				t.Fatalf("apply first observations: %v", err)
			}
			if _, err := store.CompleteRun(ctx, CompleteRun{ID: first.ID, Status: RunStatusSuccess, CompletionStatus: CompletionStatusComplete, FinishedAt: time.Now()}); err != nil {
				t.Fatalf("complete first run: %v", err)
			}
			second := startTestRun(t, store, "run-2", source.ID)
			if terminal.status == RunStatusPartial {
				if _, err := store.ApplyObservedVacancies(ctx, second.ID, observed[:1]); err != nil {
					t.Fatalf("apply partial observations: %v", err)
				}
			}
			completed, err := store.CompleteRun(ctx, CompleteRun{ID: second.ID, Status: terminal.status, CompletionStatus: terminal.completion, FinishedAt: time.Now()})
			if err != nil {
				t.Fatalf("complete %s run: %v", terminal.name, err)
			}
			if completed.ArchivedCount != 0 {
				t.Errorf("archived count = %d, want 0", completed.ArchivedCount)
			}
			var listingStatus string
			if err := store.DB.QueryRowContext(ctx, "SELECT listing_status FROM vacancies WHERE source_id = ? AND canonical_link = ?", source.ID, "https://example.test/jobs/2").Scan(&listingStatus); err != nil {
				t.Fatalf("read missing vacancy: %v", err)
			}
			if listingStatus != "active" {
				t.Errorf("listing status = %q, want active", listingStatus)
			}
		})
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

func TestCountTerminalNonFailedRunsExcludesFailedAndRunningAttempts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")

	for _, terminal := range []struct {
		id         string
		status     RunStatus
		completion CompletionStatus
	}{
		{id: "success", status: RunStatusSuccess, completion: CompletionStatusComplete},
		{id: "partial", status: RunStatusPartial, completion: CompletionStatusIncomplete},
		{id: "failed", status: RunStatusFailed, completion: CompletionStatusUnknown},
	} {
		run := startTestRun(t, store, terminal.id, source.ID)
		if _, err := store.CompleteRun(ctx, CompleteRun{
			ID: run.ID, Status: terminal.status, CompletionStatus: terminal.completion, FinishedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("complete %s run: %v", terminal.id, err)
		}
	}
	if _, err := store.StartRun(ctx, NewRun{ID: "running", SourceID: source.ID, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("start running attempt: %v", err)
	}

	count, err := store.CountTerminalNonFailedRuns(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("CountTerminalNonFailedRuns() = %d, want success and partial only", count)
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
