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
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_auto_vacuum=incremental", path)

	writer, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1) // single writer — no "database is locked"

	reader, err = sql.Open("sqlite", dsn)
	if err != nil {
		writer.Close()
		return nil, nil, fmt.Errorf("storage: open reader: %w", err)
	}
	reader.SetMaxOpenConns(4)

	if err := migrate(context.Background(), writer); err != nil {
		writer.Close()
		reader.Close()
		return nil, nil, fmt.Errorf("storage: migrate: %w", err)
	}

	return writer, reader, nil
}
