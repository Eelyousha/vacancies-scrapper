// Package runlog records the persistent history and diagnostics of scraper runs.
package runlog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"vacancies-scrapper/internal/scraper"
	"vacancies-scrapper/internal/storage"
)

// Logger finalizes already-started runs and persists their observations.
type Logger struct {
	store *storage.Store
}

// New creates a run logger backed by store.
func New(store *storage.Store) Logger {
	return Logger{store: store}
}

// Finish saves observed vacancies and turns a running record into its terminal state.
func (l Logger) Finish(ctx context.Context, runID string, result scraper.Result, fallbackFields []string, completion storage.CompletionStatus) (storage.Run, error) {
	observed := make([]storage.ObservedVacancy, len(result.Vacancies))
	for index, vacancy := range result.Vacancies {
		observed[index] = storage.ObservedVacancy{
			Title: vacancy.Title, Company: vacancy.Company, Salary: vacancy.Salary,
			Link: vacancy.Link, Description: vacancy.Description, IdentityFields: fallbackFields,
		}
	}
	delta, err := l.store.ApplyObservedVacancies(ctx, runID, observed)
	if err != nil {
		return storage.Run{}, fmt.Errorf("persist observed vacancies: %w", err)
	}

	status := storage.RunStatusSuccess
	if completion != storage.CompletionStatusComplete || len(result.DetailErrors) > 0 {
		status = storage.RunStatusPartial
		completion = storage.CompletionStatusIncomplete
	}
	finishedAt := result.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	metadata, err := resultMetadata(result)
	if err != nil {
		return storage.Run{}, err
	}
	completed, err := l.store.CompleteRun(ctx, storage.CompleteRun{
		ID: runID, FinishedAt: finishedAt, Status: status, CompletionStatus: completion,
		AddedCount: delta.AddedCount, UpdatedCount: delta.UpdatedCount,
		SeenCount: len(observed), MetaJSON: metadata,
	})
	if err != nil {
		return storage.Run{}, fmt.Errorf("complete successful run: %w", err)
	}
	return completed, nil
}

// Fail records a scraper failure after a caller has made the attempt observable.
func (l Logger) Fail(ctx context.Context, runID, message string) (storage.Run, error) {
	metadata, err := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: message})
	if err != nil {
		return storage.Run{}, fmt.Errorf("encode failed-run diagnostics: %w", err)
	}
	completed, err := l.store.CompleteRun(ctx, storage.CompleteRun{
		ID: runID, FinishedAt: time.Now().UTC(), Status: storage.RunStatusFailed,
		CompletionStatus: storage.CompletionStatusUnknown, ErrorMessage: message, MetaJSON: string(metadata),
	})
	if err != nil {
		return storage.Run{}, fmt.Errorf("complete failed run: %w", err)
	}
	return completed, nil
}

func resultMetadata(result scraper.Result) (string, error) {
	metadata, err := json.Marshal(struct {
		SiteName       string                `json:"site_name"`
		URL            string                `json:"url"`
		CardCount      int                   `json:"card_count"`
		Iterations     int                   `json:"pagination_iterations"`
		DetailsFetched int                   `json:"details_fetched"`
		DetailErrors   []scraper.DetailError `json:"detail_errors,omitempty"`
	}{
		SiteName: result.SiteName, URL: result.URL, CardCount: result.CardCount,
		Iterations: result.Iterations, DetailsFetched: result.DetailsFetched, DetailErrors: result.DetailErrors,
	})
	if err != nil {
		return "", fmt.Errorf("encode run diagnostics: %w", err)
	}
	return string(metadata), nil
}
