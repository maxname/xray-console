// Package store opens the panel's SQLite database and keeps its schema current.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Open opens (creating if needed) the database in dataDir and applies any
// migrations it has not seen yet.
func Open(ctx context.Context, dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, "xray-console.db")
	// Create the file ourselves so it is owner-only from the start: SQLite
	// would make it 0644, and gives the -wal and -shm files next to it the
	// same mode as the database.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}
	f.Close()
	return open(ctx, "file:"+path)
}

// OpenMemory opens a private in-memory database, for tests.
func OpenMemory(ctx context.Context) (*sql.DB, error) {
	db, err := open(ctx, "file::memory:")
	if err != nil {
		return nil, err
	}
	// Every connection to ":memory:" is its own database, so keep exactly one.
	db.SetMaxOpenConns(1)
	return db, nil
}

func open(ctx context.Context, dsn string) (*sql.DB, error) {
	// _txlock=immediate: a transaction takes the write lock when it begins. A
	// second writer then waits for it (busy_timeout) instead of both starting
	// as readers and one failing with SQLITE_BUSY when it tries to upgrade.
	dsn += "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	return migrateTo(ctx, db, 0)
}

// migrateTo applies the migrations up to and including version last; all of
// them when last is 0. Stopping short is for tests that need a database as an
// older panel left it.
func migrateTo(ctx context.Context, db *sql.DB, last int) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	for _, name := range names {
		// Files are named NNNN_description.sql; NNNN is the version.
		base := filepath.Base(name)
		prefix, _, _ := strings.Cut(base, "_")
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("migration %s: name must start with a number", base)
		}
		if last != 0 && version > last {
			break
		}

		var applied int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version,
		).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}

		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", base, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().Unix(),
		); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s: %w", base, err)
		}
	}
	return nil
}
