package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCreatesInitialSchema(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()

	var version int
	if err := store.DB.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 2 {
		t.Errorf("schema version = %d, want 2", version)
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
	if migrationCount != 2 {
		t.Errorf("applied migrations = %d, want 2", migrationCount)
	}
}

func TestOpenRecoversInterruptedRunningRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vacancies.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open(): %v", err)
	}
	source := createTestSource(t, store, "source-1", "example")
	if _, err := store.StartExclusiveRun(context.Background(), NewRun{ID: "interrupted-run", SourceID: source.ID, StartedAt: time.Now()}); err != nil {
		t.Fatalf("StartExclusiveRun(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store with running run: %v", err)
	}

	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer reopened.Close()
	if err := reopened.RecoverInterruptedRuns(context.Background()); err != nil {
		t.Fatalf("RecoverInterruptedRuns(): %v", err)
	}

	run, err := reopened.LastRun(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("LastRun() after recovery: %v", err)
	}
	if run.Status != RunStatusFailed || run.CompletionStatus != CompletionStatusUnknown || run.FinishedAt.IsZero() || !strings.Contains(run.ErrorMessage, "interrupted") {
		t.Errorf("recovered run = %#v, want failed interrupted run with a completion timestamp", run)
	}
	recoveredSource, err := reopened.GetSource(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("GetSource() after recovery: %v", err)
	}
	if recoveredSource.Status != SourceStatusFailed || !strings.Contains(recoveredSource.LastError, "interrupted") {
		t.Errorf("recovered source = %#v, want failed source with interruption diagnostic", recoveredSource)
	}
	if _, err := reopened.StartExclusiveRun(context.Background(), NewRun{ID: "next-run", SourceID: source.ID, StartedAt: time.Now()}); err != nil {
		t.Errorf("StartExclusiveRun() after recovery: %v", err)
	}
}

func TestOpenWithRelativePathAppliesMigrationsAndEnforcesForeignKeys(t *testing.T) {
	t.Parallel()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	databasePath, err := filepath.Rel(workingDirectory, filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatalf("make database path relative: %v", err)
	}

	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("Open() with relative path: %v", err)
	}
	defer store.Close()

	var version int
	if err := store.DB.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != LatestSchemaVersion() {
		t.Errorf("schema version = %d, want %d", version, LatestSchemaVersion())
	}

	_, err = store.DB.Exec(`
		INSERT INTO vacancies (id, source_id, title, company, link, canonical_link, identity_key, created_at, updated_at)
		VALUES ('vacancy-relative-path', 'missing-source', 'Go developer', 'Example', 'https://example.test/jobs/relative', 'https://example.test/jobs/relative', 'https://example.test/jobs/relative', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')
	`)
	if err == nil {
		t.Fatal("insert with an unknown source succeeded")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Errorf("insert error = %v, want foreign key error", err)
	}
}

func TestOpenBackfillsIdentityKeyForVersionOneDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "vacancies.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open version one database: %v", err)
	}
	initial, err := migrationFiles.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatalf("read initial migration: %v", err)
	}
	if _, err := db.Exec(string(initial)); err != nil {
		t.Fatalf("apply initial schema: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)"); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	if _, err := db.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-09-01T00:00:00Z')"); err != nil {
		t.Fatalf("record initial migration: %v", err)
	}
	if _, err := db.Exec("INSERT INTO sources (id, slug, name, config_yaml, is_active, schedule_type, status, created_at, updated_at) VALUES ('source-1', 'example', 'Example', 'site_name: Example', 1, 'manual', 'idle', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')"); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	const link = "https://example.test/jobs/1"
	if _, err := db.Exec("INSERT INTO vacancies (id, source_id, title, company, link, canonical_link, created_at, updated_at) VALUES ('vacancy-1', 'source-1', 'Role', 'Example', ?, ?, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')", link, link); err != nil {
		t.Fatalf("insert version one vacancy: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version one database: %v", err)
	}

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() migration: %v", err)
	}
	defer store.Close()
	var identityKey string
	if err := store.DB.QueryRow("SELECT identity_key FROM vacancies WHERE id = 'vacancy-1'").Scan(&identityKey); err != nil {
		t.Fatalf("read identity key: %v", err)
	}
	if identityKey != link {
		t.Errorf("identity_key = %q, want existing canonical URL %q", identityKey, link)
	}
}

func TestOpenEnforcesForeignKeys(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()

	_, err := store.DB.Exec(`
		INSERT INTO vacancies (id, source_id, title, company, link, canonical_link, identity_key, created_at, updated_at)
		VALUES ('vacancy-1', 'missing-source', 'Go developer', 'Example', 'https://example.test/jobs/1', 'https://example.test/jobs/1', 'https://example.test/jobs/1', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')
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
