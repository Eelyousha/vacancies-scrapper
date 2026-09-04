package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ObservedVacancy is a vacancy extracted during one scraper run.
type ObservedVacancy struct {
	Title       string
	Company     string
	Salary      string
	Link        string
	Description string
}

// VacancyDelta reports the dispositions recorded for one application of observations.
type VacancyDelta struct {
	AddedCount     int
	UpdatedCount   int
	UnchangedCount int
}

// ApplyObservedVacancies atomically persists the observed part of a running run.
// It intentionally does not archive absent vacancies: only a later, proven-complete
// run may make that destructive decision.
func (s *Store) ApplyObservedVacancies(ctx context.Context, runID string, observed []ObservedVacancy) (VacancyDelta, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return VacancyDelta{}, fmt.Errorf("begin vacancy application for run %q: %w", runID, err)
	}
	defer tx.Rollback()

	var sourceID string
	var status RunStatus
	if err := tx.QueryRowContext(ctx, "SELECT source_id, status FROM scraping_runs WHERE id = ?", runID).Scan(&sourceID, &status); errors.Is(err, sql.ErrNoRows) {
		return VacancyDelta{}, fmt.Errorf("run %q: %w", runID, ErrNotFound)
	} else if err != nil {
		return VacancyDelta{}, fmt.Errorf("read run %q: %w", runID, err)
	}
	if status != RunStatusRunning {
		return VacancyDelta{}, fmt.Errorf("apply vacancies to run %q: %w", runID, ErrInvalidTransition)
	}

	seen := make(map[string]struct{}, len(observed))
	var delta VacancyDelta
	for _, item := range observed {
		canonical, err := CanonicalizeLink(item.Link)
		if err != nil {
			return VacancyDelta{}, fmt.Errorf("canonicalize vacancy link %q: %w", item.Link, err)
		}
		if _, duplicate := seen[canonical]; duplicate {
			return VacancyDelta{}, fmt.Errorf("duplicate vacancy link %q", canonical)
		}
		seen[canonical] = struct{}{}

		disposition, err := applyObservedVacancy(ctx, tx, runID, sourceID, canonical, item)
		if err != nil {
			return VacancyDelta{}, err
		}
		switch disposition {
		case "added":
			delta.AddedCount++
		case "updated":
			delta.UpdatedCount++
		case "unchanged":
			delta.UnchangedCount++
		}
	}
	if err := tx.Commit(); err != nil {
		return VacancyDelta{}, fmt.Errorf("commit vacancies for run %q: %w", runID, err)
	}
	return delta, nil
}

// CanonicalizeLink produces the stable identity URL defined by the data model.
func CanonicalizeLink(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("link must be an absolute URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	parsed.RawFragment = ""
	query := parsed.Query()
	var trackingKeys []string
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "gclid" || lower == "fbclid" {
			trackingKeys = append(trackingKeys, key)
		}
	}
	// Keys are collected first: deleting while ranging over a map can skip keys.
	for _, key := range trackingKeys {
		query.Del(key)
	}
	parsed.RawQuery = query.Encode()
	parsed.ForceQuery = false
	return parsed.String(), nil
}

type storedVacancy struct {
	ID          string
	Title       string
	Company     string
	Salary      string
	Link        string
	Description string
}

func applyObservedVacancy(ctx context.Context, tx *sql.Tx, runID, sourceID, canonical string, item ObservedVacancy) (string, error) {
	var current storedVacancy
	err := tx.QueryRowContext(ctx, `
		SELECT id, title, company, COALESCE(salary, ''), link, COALESCE(description, '')
		FROM vacancies WHERE source_id = ? AND canonical_link = ?
	`, sourceID, canonical).Scan(&current.ID, &current.Title, &current.Company, &current.Salary, &current.Link, &current.Description)
	now := time.Now().UTC()
	if errors.Is(err, sql.ErrNoRows) {
		id := vacancyID(sourceID, canonical)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO vacancies (id, source_id, title, company, salary, link, canonical_link, description, last_run_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id, sourceID, item.Title, item.Company, nullIfEmpty(item.Salary), item.Link, canonical,
			nullIfEmpty(item.Description), runID, formatTime(now), formatTime(now)); err != nil {
			return "", fmt.Errorf("insert vacancy %q: %w", canonical, err)
		}
		if err := recordRunVacancy(ctx, tx, runID, id, "added"); err != nil {
			return "", err
		}
		return "added", nil
	}
	if err != nil {
		return "", fmt.Errorf("read vacancy %q: %w", canonical, err)
	}

	changed := make([]fieldChange, 0, 3)
	for _, change := range []fieldChange{
		{name: "title", old: current.Title, new: item.Title},
		{name: "company", old: current.Company, new: item.Company},
		{name: "salary", old: current.Salary, new: item.Salary},
	} {
		if change.old != change.new {
			changed = append(changed, change)
		}
	}
	updated := len(changed) > 0 || current.Description != item.Description
	if _, err := tx.ExecContext(ctx, `
		UPDATE vacancies SET title = ?, company = ?, salary = ?, description = ?, last_run_id = ?, updated_at = ? WHERE id = ?
	`, item.Title, item.Company, nullIfEmpty(item.Salary), nullIfEmpty(item.Description), runID, formatTime(now), current.ID); err != nil {
		return "", fmt.Errorf("update vacancy %q: %w", canonical, err)
	}
	for _, change := range changed {
		if err := recordHistory(ctx, tx, runID, current.ID, change, now); err != nil {
			return "", err
		}
	}
	disposition := "unchanged"
	if updated {
		disposition = "updated"
	}
	if err := recordRunVacancy(ctx, tx, runID, current.ID, disposition); err != nil {
		return "", err
	}
	return disposition, nil
}

type fieldChange struct{ name, old, new string }

func recordHistory(ctx context.Context, tx *sql.Tx, runID, vacancyID string, change fieldChange, changedAt time.Time) error {
	// A content-derived ID avoids adding a UUID generator before the application service exists.
	id := vacancyIDForHistory(runID, vacancyID, change.name)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO vacancy_history (id, vacancy_id, field_name, old_value, new_value, changed_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, vacancyID, change.name, nullIfEmpty(change.old), nullIfEmpty(change.new), formatTime(changedAt))
	if err != nil {
		return fmt.Errorf("record vacancy history for %q: %w", vacancyID, err)
	}
	return nil
}

func recordRunVacancy(ctx context.Context, tx *sql.Tx, runID, vacancyID, disposition string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO scraping_run_vacancies (run_id, vacancy_id, disposition) VALUES (?, ?, ?)`, runID, vacancyID, disposition)
	if err != nil {
		return fmt.Errorf("record run vacancy %q: %w", vacancyID, err)
	}
	return nil
}

func vacancyID(sourceID, canonical string) string {
	sum := sha256.Sum256([]byte(sourceID + "\x00" + canonical))
	return fmt.Sprintf("%x", sum)
}

func vacancyIDForHistory(runID, vacancyID, field string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + vacancyID + "\x00" + field))
	return fmt.Sprintf("%x", sum)
}
