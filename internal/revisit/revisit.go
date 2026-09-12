// Package revisit chooses a bounded collection plan for repeated source runs.
package revisit

import (
	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/storage"
)

const (
	// DefaultHeadMaxIterations keeps ordinary repeated runs short while still
	// revisiting the beginning of a dynamic listing for newly inserted cards.
	DefaultHeadMaxIterations = 3
	// DefaultFullEvery schedules a full reconciliation after this many
	// terminal, non-failed earlier runs.
	DefaultFullEvery = 5
)

// Plan is a deterministic decision for one source run. CompletionStatus is
// intentionally part of the decision: a head window cannot prove that unseen
// vacancies disappeared, so storage must not archive them.
type Plan struct {
	MaxIterations    int
	CompletionStatus storage.CompletionStatus
}

// Build chooses a head window for the first four terminal non-failed runs and
// the configured full pagination limit for every fifth run. It does not modify
// source; the lifecycle owns the copy passed to the scraper.
func Build(source config.Source, completedRuns int) Plan {
	if source.Pagination == nil {
		return Plan{CompletionStatus: storage.CompletionStatusComplete}
	}
	if completedRuns < 0 {
		completedRuns = 0
	}
	if completedRuns%DefaultFullEvery == DefaultFullEvery-1 {
		return Plan{
			MaxIterations:    source.Pagination.MaxIterations,
			CompletionStatus: storage.CompletionStatusComplete,
		}
	}
	return Plan{
		MaxIterations:    min(DefaultHeadMaxIterations, source.Pagination.MaxIterations),
		CompletionStatus: storage.CompletionStatusIncomplete,
	}
}
