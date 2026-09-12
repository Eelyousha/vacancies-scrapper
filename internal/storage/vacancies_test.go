package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplyObservedVacanciesCreatesAndClassifiesDelta(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")

	firstRun := startTestRun(t, store, "run-1", source.ID)
	observed := ObservedVacancy{Title: "Go developer", Company: "Example", Salary: "100", Link: "https://EXAMPLE.test/jobs/1?utm_source=mail#details", Description: "First"}
	first, err := store.ApplyObservedVacancies(ctx, firstRun.ID, []ObservedVacancy{observed})
	if err != nil {
		t.Fatalf("ApplyObservedVacancies(first): %v", err)
	}
	if first.AddedCount != 1 || first.UpdatedCount != 0 || first.UnchangedCount != 0 {
		t.Errorf("first delta = %#v, want one added vacancy", first)
	}

	secondRun := startTestRun(t, store, "run-2", source.ID)
	second, err := store.ApplyObservedVacancies(ctx, secondRun.ID, []ObservedVacancy{observed})
	if err != nil {
		t.Fatalf("ApplyObservedVacancies(second): %v", err)
	}
	if second.AddedCount != 0 || second.UpdatedCount != 0 || second.UnchangedCount != 1 {
		t.Errorf("second delta = %#v, want one unchanged vacancy", second)
	}

	observed.Salary = "120"
	thirdRun := startTestRun(t, store, "run-3", source.ID)
	third, err := store.ApplyObservedVacancies(ctx, thirdRun.ID, []ObservedVacancy{observed})
	if err != nil {
		t.Fatalf("ApplyObservedVacancies(third): %v", err)
	}
	if third.UpdatedCount != 1 {
		t.Errorf("third delta = %#v, want one updated vacancy", third)
	}

	var historyCount int
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM vacancy_history WHERE field_name = 'salary'").Scan(&historyCount); err != nil {
		t.Fatalf("count salary history: %v", err)
	}
	if historyCount != 1 {
		t.Errorf("salary history entries = %d, want 1", historyCount)
	}
}

func TestApplyObservedVacanciesNormalizesTrackingURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")

	first := startTestRun(t, store, "run-1", source.ID)
	firstLink := "https://example.test/jobs/1?b=2&utm_campaign=x&a=1#top"
	if _, err := store.ApplyObservedVacancies(ctx, first.ID, []ObservedVacancy{{Title: "Role", Link: firstLink}}); err != nil {
		t.Fatalf("apply first URL: %v", err)
	}
	second := startTestRun(t, store, "run-2", source.ID)
	secondLink := "https://example.test/jobs/1?a=1&b=2&gclid=ignored"
	result, err := store.ApplyObservedVacancies(ctx, second.ID, []ObservedVacancy{{Title: "Role", Link: secondLink}})
	if err != nil {
		t.Fatalf("apply canonical-equivalent URL: %v", err)
	}
	if result.UnchangedCount != 1 {
		firstCanonical, _ := CanonicalizeLink(firstLink)
		secondCanonical, _ := CanonicalizeLink(secondLink)
		t.Errorf("result = %#v, want one unchanged vacancy; canonical links: %q, %q", result, firstCanonical, secondCanonical)
	}
}

func TestApplyObservedVacanciesRejectsCompletedRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")
	run := startTestRun(t, store, "run-1", source.ID)
	if _, err := store.CompleteRun(ctx, CompleteRun{ID: run.ID, Status: RunStatusFailed, CompletionStatus: CompletionStatusUnknown, FinishedAt: time.Now()}); err != nil {
		t.Fatalf("CompleteRun(): %v", err)
	}

	_, err := store.ApplyObservedVacancies(ctx, run.ID, []ObservedVacancy{{Title: "Role", Link: "https://example.test/jobs/1"}})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("ApplyObservedVacancies() error = %v, want ErrInvalidTransition", err)
	}
}

func TestApplyObservedVacanciesUsesFallbackIdentityForDynamicURLs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")

	firstRun := startTestRun(t, store, "run-1", source.ID)
	first := ObservedVacancy{
		Title: "Go developer", Company: "Example", Link: "https://example.test/jobs/issued-1",
		IdentityFields: []string{"title", "company"},
	}
	if _, err := store.ApplyObservedVacancies(ctx, firstRun.ID, []ObservedVacancy{first}); err != nil {
		t.Fatalf("apply first observation: %v", err)
	}

	secondRun := startTestRun(t, store, "run-2", source.ID)
	second := first
	second.Link = "https://example.test/jobs/issued-2"
	delta, err := store.ApplyObservedVacancies(ctx, secondRun.ID, []ObservedVacancy{second})
	if err != nil {
		t.Fatalf("apply second observation: %v", err)
	}
	if delta.UnchangedCount != 1 || delta.AddedCount != 0 {
		t.Errorf("delta = %#v, want the same fallback-identified vacancy", delta)
	}

	thirdRun := startTestRun(t, store, "run-3", source.ID)
	second.Title = "Senior Go developer"
	delta, err = store.ApplyObservedVacancies(ctx, thirdRun.ID, []ObservedVacancy{second})
	if err != nil {
		t.Fatalf("apply changed fallback observation: %v", err)
	}
	if delta.AddedCount != 1 {
		t.Errorf("delta = %#v, want a new vacancy after fallback change", delta)
	}
}

func TestApplyObservedVacanciesDeduplicatesRepeatedIdentityWithinRun(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name     string
		observed []ObservedVacancy
	}{
		{
			name: "canonical URL",
			observed: []ObservedVacancy{
				{Title: "Dispatcher", Company: "Example", Link: "https://example.test/jobs/1?utm_source=card"},
				{Title: "Dispatcher duplicate", Company: "Another", Link: "https://example.test/jobs/1"},
			},
		},
		{
			name: "fallback fingerprint",
			observed: []ObservedVacancy{
				{Title: "Dispatcher", Company: "Example", Link: "https://example.test/jobs/1", IdentityFields: []string{"title", "company"}},
				{Title: "Dispatcher", Company: "Example", Link: "https://example.test/jobs/2", IdentityFields: []string{"title", "company"}},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := openTestStore(t)
			defer store.Close()
			source := createTestSource(t, store, "source-1", "example")
			run := startTestRun(t, store, "run-1", source.ID)

			delta, err := store.ApplyObservedVacancies(ctx, run.ID, testCase.observed)
			if err != nil {
				t.Fatalf("ApplyObservedVacancies(): %v", err)
			}
			if delta != (VacancyDelta{AddedCount: 1}) {
				t.Errorf("delta = %#v, want one added vacancy", delta)
			}

			var vacancies, runVacancies int
			if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM vacancies WHERE source_id = ?", source.ID).Scan(&vacancies); err != nil {
				t.Fatalf("count vacancies: %v", err)
			}
			if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scraping_run_vacancies WHERE run_id = ?", run.ID).Scan(&runVacancies); err != nil {
				t.Fatalf("count run vacancies: %v", err)
			}
			if vacancies != 1 || runVacancies != 1 {
				t.Errorf("persisted vacancies/run-vacancies = %d/%d, want 1/1", vacancies, runVacancies)
			}
		})
	}
}

func TestApplyObservedVacanciesReactivatesArchivedVacancy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")
	observed := ObservedVacancy{Title: "Go developer", Company: "Example", Link: "https://example.test/jobs/1"}

	first := startTestRun(t, store, "run-1", source.ID)
	if _, err := store.ApplyObservedVacancies(ctx, first.ID, []ObservedVacancy{observed}); err != nil {
		t.Fatalf("apply first observation: %v", err)
	}
	if _, err := store.CompleteRun(ctx, CompleteRun{ID: first.ID, Status: RunStatusSuccess, CompletionStatus: CompletionStatusComplete, FinishedAt: time.Now()}); err != nil {
		t.Fatalf("complete first run: %v", err)
	}

	second := startTestRun(t, store, "run-2", source.ID)
	if _, err := store.CompleteRun(ctx, CompleteRun{ID: second.ID, Status: RunStatusSuccess, CompletionStatus: CompletionStatusComplete, FinishedAt: time.Now()}); err != nil {
		t.Fatalf("complete empty run: %v", err)
	}
	var archived string
	if err := store.DB.QueryRowContext(ctx, "SELECT listing_status FROM vacancies WHERE source_id = ?", source.ID).Scan(&archived); err != nil {
		t.Fatalf("read archived vacancy: %v", err)
	}
	if archived != "archived" {
		t.Fatalf("listing status after empty complete run = %q, want archived", archived)
	}

	third := startTestRun(t, store, "run-3", source.ID)
	delta, err := store.ApplyObservedVacancies(ctx, third.ID, []ObservedVacancy{observed})
	if err != nil {
		t.Fatalf("apply returning observation: %v", err)
	}
	if delta.UpdatedCount != 1 {
		t.Errorf("returning observation delta = %#v, want one updated vacancy", delta)
	}
	var status string
	var archivedAt any
	if err := store.DB.QueryRowContext(ctx, "SELECT listing_status, archived_at FROM vacancies WHERE source_id = ?", source.ID).Scan(&status, &archivedAt); err != nil {
		t.Fatalf("read reactivated vacancy: %v", err)
	}
	if status != "active" {
		t.Errorf("listing status after returning observation = %q, want active", status)
	}
	if archivedAt != nil {
		t.Errorf("archived_at after returning observation = %v, want NULL", archivedAt)
	}
}

func startTestRun(t *testing.T, store *Store, id, sourceID string) Run {
	t.Helper()
	run, err := store.StartRun(context.Background(), NewRun{ID: id, SourceID: sourceID, StartedAt: time.Now()})
	if err != nil {
		t.Fatalf("StartRun(): %v", err)
	}
	return run
}
