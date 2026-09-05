package storage

import (
	"context"
	"testing"
	"time"
)

func TestListVacanciesFiltersAndUpdatesUserStatus(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	source := createTestSource(t, store, "source-1", "example")
	run := startTestRun(t, store, "run-1", source.ID)
	if _, err := store.ApplyObservedVacancies(ctx, run.ID, []ObservedVacancy{{Title: "Go developer", Company: "Example", Link: "https://example.test/1"}, {Title: "Designer", Company: "Example", Link: "https://example.test/2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteRun(ctx, CompleteRun{ID: run.ID, Status: RunStatusSuccess, CompletionStatus: CompletionStatusComplete, FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	items, total, err := store.ListVacancies(ctx, VacancyFilter{SourceID: source.ID, Search: "go", Limit: 10})
	if err != nil || total != 1 || len(items) != 1 || items[0].Title != "Go developer" {
		t.Fatalf("ListVacancies() = %#v, %d, %v", items, total, err)
	}
	updated, err := store.UpdateVacancyUserStatus(ctx, items[0].ID, "viewed")
	if err != nil || updated.UserStatus != "viewed" {
		t.Fatalf("UpdateVacancyUserStatus() = %#v, %v", updated, err)
	}
}
