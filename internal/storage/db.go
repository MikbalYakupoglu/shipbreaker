package storage

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open returns a writer DB (single connection) and a reader pool.
// WAL mode + busy_timeout + foreign keys are configured on both.
func Open(path string) (writer *sql.DB, reader *sql.DB, err error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=on&_auto_vacuum=incremental&_busy_timeout=5000", path)

	writer, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)

	if err := applyPragmas(context.Background(), writer); err != nil {
		writer.Close()
		return nil, nil, fmt.Errorf("storage: writer pragmas: %w", err)
	}

	reader, err = sql.Open("sqlite", dsn)
	if err != nil {
		writer.Close()
		return nil, nil, fmt.Errorf("storage: open reader: %w", err)
	}
	reader.SetMaxOpenConns(4)

	if err := applyPragmas(context.Background(), reader); err != nil {
		writer.Close()
		reader.Close()
		return nil, nil, fmt.Errorf("storage: reader pragmas: %w", err)
	}

	if err := migrate(context.Background(), writer); err != nil {
		writer.Close()
		reader.Close()
		return nil, nil, fmt.Errorf("storage: migrate: %w", err)
	}

	return writer, reader, nil
}

// applyPragmas sets per-connection SQLite pragmas that must be applied
// directly rather than via DSN, since modernc.org/sqlite does not
// guarantee DSN-encoded pragmas are applied to every physical connection.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	pragmas := []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}
