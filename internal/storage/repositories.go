package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrNotFound lets callers distinguish an absent record from a database failure.
	ErrNotFound = errors.New("storage record not found")
	// ErrInvalidTransition means an operation does not match the record's current state.
	ErrInvalidTransition = errors.New("invalid storage state transition")
)

type SourceStatus string

const (
	SourceStatusIdle    SourceStatus = "idle"
	SourceStatusRunning SourceStatus = "running"
	SourceStatusSuccess SourceStatus = "success"
	SourceStatusFailed  SourceStatus = "failed"
	SourceStatusPartial SourceStatus = "partial"
)

type RunStatus string

const (
	RunStatusRunning RunStatus = "running"
	RunStatusSuccess RunStatus = "success"
	RunStatusFailed  RunStatus = "failed"
	RunStatusPartial RunStatus = "partial"
)

type CompletionStatus string

const (
	CompletionStatusComplete   CompletionStatus = "complete"
	CompletionStatusIncomplete CompletionStatus = "incomplete"
	CompletionStatusUnknown    CompletionStatus = "unknown"
)

// Source is the persistent configuration and current operational state of a site.
type Source struct {
	ID               string
	Slug             string
	Name             string
	ConfigYAML       string
	IsActive         bool
	ScheduleType     string
	ScheduleValue    string
	ScheduleTimezone string
	LastRunAt        time.Time
	LastError        string
	Status           SourceStatus
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// NewSource contains fields set by the caller when registering a site.
type NewSource struct {
	ID               string
	Slug             string
	Name             string
	ConfigYAML       string
	IsActive         bool
	ScheduleType     string
	ScheduleValue    string
	ScheduleTimezone string
}

// Run records one attempt to collect vacancies from a source.
type Run struct {
	ID               string
	SourceID         string
	StartedAt        time.Time
	FinishedAt       time.Time
	Status           RunStatus
	CompletionStatus CompletionStatus
	AddedCount       int
	UpdatedCount     int
	ArchivedCount    int
	SeenCount        int
	ErrorMessage     string
	MetaJSON         string
}

type NewRun struct {
	ID        string
	SourceID  string
	StartedAt time.Time
}

type CompleteRun struct {
	ID               string
	FinishedAt       time.Time
	Status           RunStatus
	CompletionStatus CompletionStatus
	AddedCount       int
	UpdatedCount     int
	ArchivedCount    int
	SeenCount        int
	ErrorMessage     string
	MetaJSON         string
}

// CreateSource persists a source with an idle status and timestamps assigned by
// the storage layer, so all callers use the same UTC clock convention.
func (s *Store) CreateSource(ctx context.Context, input NewSource) (Source, error) {
	if err := validateNewSource(input); err != nil {
		return Source{}, err
	}
	now := time.Now().UTC()
	scheduleType := input.ScheduleType
	if scheduleType == "" {
		scheduleType = "manual"
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO sources (
			id, slug, name, config_yaml, is_active, schedule_type, schedule_value,
			schedule_timezone, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, input.ID, input.Slug, input.Name, input.ConfigYAML, input.IsActive, scheduleType,
		nullIfEmpty(input.ScheduleValue), nullIfEmpty(input.ScheduleTimezone), SourceStatusIdle,
		formatTime(now), formatTime(now))
	if err != nil {
		return Source{}, fmt.Errorf("create source: %w", err)
	}
	return s.GetSource(ctx, input.ID)
}

// GetSource returns a source by its stable identifier.
func (s *Store) GetSource(ctx context.Context, id string) (Source, error) {
	row := s.DB.QueryRowContext(ctx, sourceSelect+" WHERE id = ?", id)
	source, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, fmt.Errorf("source %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Source{}, fmt.Errorf("get source %q: %w", id, err)
	}
	return source, nil
}

// GetSourceBySlug returns a source by its human-readable unique key.
func (s *Store) GetSourceBySlug(ctx context.Context, slug string) (Source, error) {
	row := s.DB.QueryRowContext(ctx, sourceSelect+" WHERE slug = ?", slug)
	source, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, fmt.Errorf("source slug %q: %w", slug, ErrNotFound)
	}
	if err != nil {
		return Source{}, fmt.Errorf("get source slug %q: %w", slug, err)
	}
	return source, nil
}

// UpdateSource changes configuration fields but preserves operational state,
// which is owned exclusively by run lifecycle methods.
func (s *Store) UpdateSource(ctx context.Context, source Source) (Source, error) {
	if err := validateSourceUpdate(source); err != nil {
		return Source{}, err
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE sources
		SET slug = ?, name = ?, config_yaml = ?, is_active = ?, schedule_type = ?,
			schedule_value = ?, schedule_timezone = ?, updated_at = ?
		WHERE id = ?
	`, source.Slug, source.Name, source.ConfigYAML, source.IsActive, source.ScheduleType,
		nullIfEmpty(source.ScheduleValue), nullIfEmpty(source.ScheduleTimezone),
		formatTime(time.Now().UTC()), source.ID)
	if err != nil {
		return Source{}, fmt.Errorf("update source %q: %w", source.ID, err)
	}
	if err := requireAffectedRow(result); err != nil {
		return Source{}, fmt.Errorf("update source %q: %w", source.ID, err)
	}
	return s.GetSource(ctx, source.ID)
}

// StartRun atomically records a running attempt and reflects that state on its source.
func (s *Store) StartRun(ctx context.Context, input NewRun) (Run, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.SourceID) == "" {
		return Run{}, fmt.Errorf("run id and source id are required")
	}
	if input.StartedAt.IsZero() {
		input.StartedAt = time.Now()
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("begin run %q: %w", input.ID, err)
	}
	defer tx.Rollback()

	// Updating the source first provides a clear not-found error instead of
	// leaking a driver-specific foreign-key failure from the run insert.
	result, err := tx.ExecContext(ctx,
		"UPDATE sources SET status = ?, updated_at = ? WHERE id = ?",
		SourceStatusRunning, formatTime(time.Now().UTC()), input.SourceID)
	if err != nil {
		return Run{}, fmt.Errorf("mark source %q running: %w", input.SourceID, err)
	}
	if err := requireAffectedRow(result); err != nil {
		return Run{}, fmt.Errorf("start run for source %q: %w", input.SourceID, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO scraping_runs (id, source_id, started_at, status, completion_status)
		VALUES (?, ?, ?, ?, ?)
	`, input.ID, input.SourceID, formatTime(input.StartedAt), RunStatusRunning, CompletionStatusUnknown); err != nil {
		return Run{}, fmt.Errorf("create run %q: %w", input.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("commit run %q: %w", input.ID, err)
	}
	return s.getRun(ctx, input.ID)
}

// CompleteRun permits exactly one terminal update for a running run and then
// synchronises the source's latest status and diagnostics in the same transaction.
// A complete successful run also archives active vacancies it did not observe.
func (s *Store) CompleteRun(ctx context.Context, input CompleteRun) (Run, error) {
	if err := validateCompletion(input); err != nil {
		return Run{}, err
	}
	if input.FinishedAt.IsZero() {
		input.FinishedAt = time.Now()
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("begin completion for run %q: %w", input.ID, err)
	}
	defer tx.Rollback()

	var sourceID string
	if err := tx.QueryRowContext(ctx, "SELECT source_id FROM scraping_runs WHERE id = ?", input.ID).Scan(&sourceID); errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("run %q: %w", input.ID, ErrNotFound)
	} else if err != nil {
		return Run{}, fmt.Errorf("read run %q: %w", input.ID, err)
	}
	if input.Status == RunStatusSuccess && input.CompletionStatus == CompletionStatusComplete {
		archivedCount, err := archiveUnseenVacancies(ctx, tx, input.ID, sourceID, input.FinishedAt)
		if err != nil {
			return Run{}, err
		}
		// Only this transaction decides archival, preventing a caller from
		// claiming archived rows after a partial or failed collection.
		input.ArchivedCount = archivedCount
	} else {
		input.ArchivedCount = 0
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE scraping_runs
		SET finished_at = ?, status = ?, completion_status = ?, added_count = ?,
			updated_count = ?, archived_count = ?, seen_count = ?, error_message = ?, meta_json = ?
		WHERE id = ? AND status = ?
	`, formatTime(input.FinishedAt), input.Status, input.CompletionStatus, input.AddedCount,
		input.UpdatedCount, input.ArchivedCount, input.SeenCount, nullIfEmpty(input.ErrorMessage),
		nullIfEmpty(input.MetaJSON), input.ID, RunStatusRunning)
	if err != nil {
		return Run{}, fmt.Errorf("complete run %q: %w", input.ID, err)
	}
	if err := requireAffectedRow(result); err != nil {
		return Run{}, fmt.Errorf("complete run %q: %w", input.ID, ErrInvalidTransition)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE sources
		SET status = ?, last_run_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?
	`, SourceStatus(input.Status), formatTime(input.FinishedAt), nullIfEmpty(input.ErrorMessage),
		formatTime(time.Now().UTC()), sourceID); err != nil {
		return Run{}, fmt.Errorf("update source %q after run %q: %w", sourceID, input.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("commit completion for run %q: %w", input.ID, err)
	}
	return s.getRun(ctx, input.ID)
}

func archiveUnseenVacancies(ctx context.Context, tx *sql.Tx, runID, sourceID string, archivedAt time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, `
		UPDATE vacancies
		SET listing_status = 'archived', archived_at = ?, updated_at = ?
		WHERE source_id = ? AND listing_status = 'active'
			AND (last_run_id IS NULL OR last_run_id != ?)
		RETURNING id
	`, formatTime(archivedAt), formatTime(archivedAt), sourceID, runID)
	if err != nil {
		return 0, fmt.Errorf("archive unseen vacancies for run %q: %w", runID, err)
	}
	defer rows.Close()

	archivedCount := 0
	for rows.Next() {
		var vacancyID string
		if err := rows.Scan(&vacancyID); err != nil {
			return 0, fmt.Errorf("read archived vacancy for run %q: %w", runID, err)
		}
		if err := recordRunVacancy(ctx, tx, runID, vacancyID, "archived"); err != nil {
			return 0, err
		}
		archivedCount++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate archived vacancies for run %q: %w", runID, err)
	}
	return archivedCount, nil
}

// LastRun returns the newest run by start time for a particular source.
func (s *Store) LastRun(ctx context.Context, sourceID string) (Run, error) {
	run, err := scanRun(s.DB.QueryRowContext(ctx, runSelect+" WHERE source_id = ? ORDER BY started_at DESC LIMIT 1", sourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("last run for source %q: %w", sourceID, ErrNotFound)
	}
	if err != nil {
		return Run{}, fmt.Errorf("last run for source %q: %w", sourceID, err)
	}
	return run, nil
}

func (s *Store) getRun(ctx context.Context, id string) (Run, error) {
	run, err := scanRun(s.DB.QueryRowContext(ctx, runSelect+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("run %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Run{}, fmt.Errorf("get run %q: %w", id, err)
	}
	return run, nil
}

const sourceSelect = `SELECT id, slug, name, config_yaml, is_active, schedule_type,
	COALESCE(schedule_value, ''), COALESCE(schedule_timezone, ''),
	COALESCE(last_run_at, ''), COALESCE(last_error, ''), status, created_at, updated_at FROM sources`

func scanSource(row *sql.Row) (Source, error) {
	var source Source
	var active int
	var lastRunAt, createdAt, updatedAt string
	if err := row.Scan(&source.ID, &source.Slug, &source.Name, &source.ConfigYAML, &active,
		&source.ScheduleType, &source.ScheduleValue, &source.ScheduleTimezone, &lastRunAt,
		&source.LastError, &source.Status, &createdAt, &updatedAt); err != nil {
		return Source{}, err
	}
	var err error
	if source.LastRunAt, err = parseOptionalTime(lastRunAt); err != nil {
		return Source{}, fmt.Errorf("parse source last_run_at: %w", err)
	}
	if source.CreatedAt, err = parseTime(createdAt); err != nil {
		return Source{}, fmt.Errorf("parse source created_at: %w", err)
	}
	if source.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Source{}, fmt.Errorf("parse source updated_at: %w", err)
	}
	source.IsActive = active != 0
	return source, nil
}

const runSelect = `SELECT id, source_id, started_at, COALESCE(finished_at, ''), status,
	completion_status, added_count, updated_count, archived_count, seen_count,
	COALESCE(error_message, ''), COALESCE(meta_json, '') FROM scraping_runs`

func scanRun(row *sql.Row) (Run, error) {
	var run Run
	var startedAt, finishedAt string
	if err := row.Scan(&run.ID, &run.SourceID, &startedAt, &finishedAt, &run.Status,
		&run.CompletionStatus, &run.AddedCount, &run.UpdatedCount, &run.ArchivedCount,
		&run.SeenCount, &run.ErrorMessage, &run.MetaJSON); err != nil {
		return Run{}, err
	}
	var err error
	if run.StartedAt, err = parseTime(startedAt); err != nil {
		return Run{}, fmt.Errorf("parse run started_at: %w", err)
	}
	if run.FinishedAt, err = parseOptionalTime(finishedAt); err != nil {
		return Run{}, fmt.Errorf("parse run finished_at: %w", err)
	}
	return run, nil
}

func validateNewSource(input NewSource) error {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Slug) == "" ||
		strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.ConfigYAML) == "" {
		return fmt.Errorf("source id, slug, name and config YAML are required")
	}
	if input.ScheduleType != "" && input.ScheduleType != "manual" && input.ScheduleType != "cron" && input.ScheduleType != "interval" {
		return fmt.Errorf("unsupported schedule type %q", input.ScheduleType)
	}
	return nil
}

func validateSourceUpdate(source Source) error {
	return validateNewSource(NewSource{
		ID: source.ID, Slug: source.Slug, Name: source.Name, ConfigYAML: source.ConfigYAML,
		ScheduleType: source.ScheduleType,
	})
}

func validateCompletion(input CompleteRun) error {
	if strings.TrimSpace(input.ID) == "" {
		return fmt.Errorf("run id is required")
	}
	if input.Status != RunStatusSuccess && input.Status != RunStatusFailed && input.Status != RunStatusPartial {
		return fmt.Errorf("run %q cannot complete with status %q", input.ID, input.Status)
	}
	if input.CompletionStatus != CompletionStatusComplete && input.CompletionStatus != CompletionStatusIncomplete && input.CompletionStatus != CompletionStatusUnknown {
		return fmt.Errorf("run %q has unsupported completion status %q", input.ID, input.CompletionStatus)
	}
	return nil
}

func requireAffectedRow(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return parseTime(value)
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
