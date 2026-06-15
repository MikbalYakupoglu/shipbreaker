package storage

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open returns a writer DB (single connection) and a reader pool.
// All per-connection pragmas are embedded in the DSN using the _pragma= format,
// which modernc.org/sqlite v1.34+ applies to every new physical connection via
// applyQueryParams — unlike the older _busy_timeout= / _journal_mode= style
// params that are not guaranteed to be applied per-connection.
func Open(path string) (writer *sql.DB, reader *sql.DB, err error) {
	// _pragma= values are applied to every new connection by modernc.org/sqlite.
	// journal_mode is database-level (persists), but specifying it here ensures
	// it is set even on a brand-new database before any other connection opens.
	// busy_timeout must be per-connection; without it SQLITE_BUSY is returned
	// immediately on any contention instead of retrying for 5 s.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=auto_vacuum(INCREMENTAL)",
		path,
	)

	writer, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)

	reader, err = sql.Open("sqlite", dsn)
	if err != nil {
		writer.Close()
		return nil, nil, fmt.Errorf("storage: open reader: %w", err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)

	if err := migrate(context.Background(), writer); err != nil {
		writer.Close()
		reader.Close()
		return nil, nil, fmt.Errorf("storage: migrate: %w", err)
	}

	return writer, reader, nil
}
