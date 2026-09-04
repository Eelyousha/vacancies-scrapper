// Package storage owns the SQLite connection and the versioned database schema.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"sort"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store is the application's access point to a migrated SQLite database.
// Repositories will be added here in subsequent increments.
type Store struct {
	DB *sql.DB
}

type migration struct {
	version int
	path    string
}

var migrations = []migration{
	{version: 1, path: "migrations/001_initial.sql"},
}

// Open connects to one SQLite file, enables foreign-key enforcement, and brings
// its schema to the latest version before returning it to the caller.
func Open(ctx context.Context, path string) (*Store, error) {
	// The pragma in the DSN applies to every connection opened by database/sql;
	// setting it below as well lets Open fail early if the driver ignores it.
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     path,
		RawQuery: url.Values{"_pragma": {"foreign_keys(1)"}}.Encode(),
	}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	if err := applyMigrations(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

// Close releases all SQLite connections held by the store.
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	// Sorting keeps migration application deterministic even if entries are later
	// declared in a different order than their filenames.
	sorted := append([]migration(nil), migrations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].version < sorted[j].version })
	for _, item := range sorted {
		var exists bool
		err := db.QueryRowContext(
			ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)",
			item.version,
		).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", item.version, err)
		}
		if exists {
			continue
		}

		sqlBytes, err := migrationFiles.ReadFile(item.path)
		if err != nil {
			return fmt.Errorf("read migration %d: %w", item.version, err)
		}
		if err := applyMigration(ctx, db, item.version, string(sqlBytes)); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, version int, statement string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", version, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("apply migration %d: %w", version, err)
	}
	if _, err := tx.ExecContext(
		ctx,
		"INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))",
		version,
	); err != nil {
		return fmt.Errorf("record migration %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", version, err)
	}
	return nil
}

// LatestSchemaVersion is useful to health checks and later CLI diagnostics.
func LatestSchemaVersion() int {
	return migrations[len(migrations)-1].version
}
