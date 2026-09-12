package revisit

import (
	"testing"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/storage"
)

func TestBuildUsesHeadWindowUntilEveryFifthCompletedRun(t *testing.T) {
	source := config.Source{Pagination: &config.Pagination{MaxIterations: 8}}

	for completedRuns := 0; completedRuns < DefaultFullEvery-1; completedRuns++ {
		plan := Build(source, completedRuns)
		if plan.MaxIterations != DefaultHeadMaxIterations {
			t.Errorf("Build(..., %d).MaxIterations = %d, want head limit %d", completedRuns, plan.MaxIterations, DefaultHeadMaxIterations)
		}
		if plan.CompletionStatus != storage.CompletionStatusIncomplete {
			t.Errorf("Build(..., %d).CompletionStatus = %q, want incomplete", completedRuns, plan.CompletionStatus)
		}
	}

	plan := Build(source, DefaultFullEvery-1)
	if plan.MaxIterations != source.Pagination.MaxIterations {
		t.Errorf("Build(..., %d).MaxIterations = %d, want configured full limit %d", DefaultFullEvery-1, plan.MaxIterations, source.Pagination.MaxIterations)
	}
	if plan.CompletionStatus != storage.CompletionStatusComplete {
		t.Errorf("Build(..., %d).CompletionStatus = %q, want complete", DefaultFullEvery-1, plan.CompletionStatus)
	}
}

func TestBuildNeverExpandsConfiguredPaginationLimit(t *testing.T) {
	source := config.Source{Pagination: &config.Pagination{MaxIterations: 2}}

	plan := Build(source, 0)
	if plan.MaxIterations != 2 {
		t.Errorf("head plan MaxIterations = %d, want configured limit 2", plan.MaxIterations)
	}
	if plan.CompletionStatus != storage.CompletionStatusIncomplete {
		t.Errorf("head plan completion = %q, want incomplete", plan.CompletionStatus)
	}
}

func TestBuildWithoutPaginationIsAlwaysComplete(t *testing.T) {
	source := config.Source{}

	for _, completedRuns := range []int{0, 4, 9} {
		plan := Build(source, completedRuns)
		if plan.MaxIterations != 0 {
			t.Errorf("Build(no pagination, %d).MaxIterations = %d, want 0", completedRuns, plan.MaxIterations)
		}
		if plan.CompletionStatus != storage.CompletionStatusComplete {
			t.Errorf("Build(no pagination, %d).CompletionStatus = %q, want complete", completedRuns, plan.CompletionStatus)
		}
	}
}
