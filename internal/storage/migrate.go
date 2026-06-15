package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{version: 1, sql: schema},
	{version: 2, sql: schemaV2},
}

const schema = `
CREATE TABLE IF NOT EXISTS containers (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    image           TEXT NOT NULL,
    image_repo      TEXT,
    image_id        TEXT,
    compose_project TEXT,
    compose_service TEXT,
    service_key     TEXT NOT NULL,
    service_id      TEXT NOT NULL,
    status          TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    first_seen_at   INTEGER NOT NULL,
    last_seen_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS metrics_raw (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    container_id    TEXT    NOT NULL REFERENCES containers(id),
    timestamp       INTEGER NOT NULL,
    cpu_percent     REAL    NOT NULL,
    memory_ws_bytes INTEGER NOT NULL,
    net_rx_bytes    INTEGER NOT NULL,
    net_tx_bytes    INTEGER NOT NULL,
    blk_read_bytes  INTEGER NOT NULL,
    blk_write_bytes INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS metrics_hourly (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    container_id      TEXT    NOT NULL REFERENCES containers(id),
    bucket            INTEGER NOT NULL,
    cpu_percent_avg   REAL    NOT NULL,
    cpu_percent_max   REAL    NOT NULL,
    memory_ws_avg     INTEGER NOT NULL,
    net_rx_delta      INTEGER NOT NULL,
    net_tx_delta      INTEGER NOT NULL,
    blk_read_delta    INTEGER NOT NULL,
    blk_write_delta   INTEGER NOT NULL,
    sample_count      INTEGER NOT NULL,
    UNIQUE(container_id, bucket)
);

CREATE INDEX IF NOT EXISTS idx_raw_container_time    ON metrics_raw(container_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_hourly_container_time ON metrics_hourly(container_id, bucket);
CREATE INDEX IF NOT EXISTS idx_containers_service    ON containers(service_id, created_at);
CREATE INDEX IF NOT EXISTS idx_raw_time              ON metrics_raw(timestamp);
CREATE INDEX IF NOT EXISTS idx_hourly_bucket         ON metrics_hourly(bucket);
`

const schemaV2 = `
CREATE TABLE IF NOT EXISTS app_settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

func migrate(ctx context.Context, db *sql.DB) error {
	// Ensure schema_migrations exists first (chicken-and-egg)
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return err
	}

	for _, m := range migrations {
		var count int
		err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.version).
			Scan(&count)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", m.version, err)
		}
		if count > 0 {
			continue
		}

		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("apply migration %d: %w", m.version, err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
			m.version, time.Now().UTC().Unix()); err != nil {
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
	}
	return nil
}
