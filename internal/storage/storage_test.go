package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesInitialSchema(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()

	var version int
	if err := store.DB.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema version = %d, want 1", version)
	}

	for _, table := range []string{"sources", "vacancies", "vacancy_history", "scraping_runs", "scraping_run_vacancies"} {
		var name string
		err := store.DB.QueryRow(
			"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?",
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q was not created: %v", table, err)
		}
	}
}

func TestOpenAppliesMigrationsOnlyOnce(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vacancies.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open(): %v", err)
	}
	defer second.Close()

	var migrationCount int
	if err := second.DB.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrationCount != 1 {
		t.Errorf("applied migrations = %d, want 1", migrationCount)
	}
}

func TestOpenEnforcesForeignKeys(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()

	_, err := store.DB.Exec(`
		INSERT INTO vacancies (id, source_id, title, company, link, canonical_link, created_at, updated_at)
		VALUES ('vacancy-1', 'missing-source', 'Go developer', 'Example', 'https://example.test/jobs/1', 'https://example.test/jobs/1', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')
	`)
	if err == nil {
		t.Fatal("insert with an unknown source succeeded")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Errorf("insert error = %v, want foreign key error", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	return store
}
